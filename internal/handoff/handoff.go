// Package handoff runs the media hand-off to analysis (REQ-010 fix 12, the
// contract's securebrand.media.v1; owner R5): each stored image of the
// handed-off kinds is announced on content.media.ready.v1 with a deadline;
// the consumers' acknowledgements arrive on content.media.processed.v1
// (yts reads its own, platform "yt"); once every required consumer has
// acknowledged a file, or its deadline passes, the file is deleted from
// storage and content.media.deleted.v1 says so. The media row (the
// record) stays forever.
//
// It runs in the relay, so without Kafka (the pilot) nothing is announced
// and nothing is deleted: images are kept until they can be analysed.
package handoff

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
)

// TopicProcessed is where consumers acknowledge files.
const TopicProcessed = "content.media.processed.v1"

// MaxAttempt: the first announcement, and at most 3 more after a
// retryable failure (the contract).
const MaxAttempt = 4

// DefaultKeep is how long an announced file waits for its acknowledgements
// (the contract's default; at least MinKeep).
const (
	DefaultKeep = 72 * time.Hour
	MinKeep     = 24 * time.Hour
)

// Handoff runs the hand-off.
type Handoff struct {
	Pool      *pgxpool.Pool
	Store     media.Store
	Kinds     []string      // the handed-off kinds (media.HandoffKinds)
	Consumers []string      // the consumers that must acknowledge ("sb-analysis")
	Keep      time.Duration // an announced file's deadline (DefaultKeep)
	Log       *slog.Logger
	Metrics   *metrics.Registry // nil: none
	Now       func() time.Time
}

func (h *Handoff) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handoff) keep() time.Duration {
	if h.Keep < MinKeep {
		return DefaultKeep
	}
	return h.Keep
}

func (h *Handoff) add(name, help string, labels map[string]string) {
	if h.Metrics != nil {
		h.Metrics.Add(name, help, labels, 1)
	}
}

// Announce gives up to limit stored images not yet announced their
// deadline and their content.media.ready event, oldest first. It returns
// how many.
func (h *Handoff) Announce(ctx context.Context, limit int) (int, error) {
	now := h.now()
	ev := outbox.New(ctx, now)
	var keys []outbox.MediaKey
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH picked AS (
				SELECT kind, entity_id, sha256 FROM media
				WHERE kind = ANY($1) AND announced_at IS NULL AND deleted_at IS NULL
				ORDER BY first_seen LIMIT $2 FOR UPDATE SKIP LOCKED)
			UPDATE media m SET announced_at = $3::timestamptz, delete_after = $3::timestamptz + make_interval(secs => $4::double precision), attempt = 1
			FROM picked p WHERE m.kind = p.kind AND m.entity_id = p.entity_id AND m.sha256 = p.sha256
			RETURNING m.kind, m.entity_id, m.sha256`, h.Kinds, limit, now, h.keep().Seconds())
		if err != nil {
			return err
		}
		keys, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (outbox.MediaKey, error) {
			var k outbox.MediaKey
			return k, r.Scan(&k.Kind, &k.EntityID, &k.SHA256)
		})
		if err != nil {
			return err
		}
		_, err = ev.MediaEvent(ctx, tx, outbox.KindMediaReady, keys...)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("handoff: announcing: %w", err)
	}
	for range keys {
		h.add("yt_media_announced_total", "Images announced to analysis (content.media.ready).", nil)
	}
	return len(keys), nil
}

var statuses = map[mediav1.ProcessStatus]string{
	mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED:        "processed",
	mediav1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT: "failed_permanent",
	mediav1.ProcessStatus_PROCESS_STATUS_FAILED_RETRYABLE: "failed_retryable",
	mediav1.ProcessStatus_PROCESS_STATUS_SKIPPED:          "skipped",
}

// parseID reads "yt:media:<kind>:<entity>:<first 16 hex of the SHA-256>".
func parseID(id string) (kind, entity, sha16 string, ok bool) {
	rest, found := strings.CutPrefix(id, "yt:media:")
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || len(parts[2]) != 16 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// Process records one acknowledgement. Another platform's, a consumer not
// required, and a file yts doesn't have (erased) are ignored. After a
// retryable failure the file is announced again, at most MaxAttempt times
// in all.
func (h *Handoff) Process(ctx context.Context, m *mediav1.MediaProcessed) error {
	if m.GetPlatform() != "yt" {
		return nil
	}
	kind, entity, sha16, ok := parseID(m.GetMediaId())
	status, known := statuses[m.GetStatus()]
	if !ok || !known || !slices.Contains(h.Consumers, m.GetConsumer()) {
		h.Log.Warn("media acknowledgement ignored", "media", m.GetMediaId(), "consumer", m.GetConsumer(), "status", m.GetStatus().String())
		return nil
	}
	now := h.now()
	ev := outbox.New(ctx, now)
	var processedAt *time.Time
	if m.GetProcessedAt() != nil {
		t := m.GetProcessedAt().AsTime()
		processedAt = &t
	}
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		var k outbox.MediaKey
		var attempt int
		var deleted *time.Time
		err := tx.QueryRow(ctx, `SELECT kind, entity_id, sha256, attempt, deleted_at FROM media
			WHERE kind = $1 AND entity_id = $2 AND left(sha256, 16) = $3 FOR UPDATE`, kind, entity, sha16).
			Scan(&k.Kind, &k.EntityID, &k.SHA256, &attempt, &deleted)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // erased, or not one of ours
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO media_acks (kind, entity_id, sha256, consumer, status, reason, error, processed_at, received_at)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9)
			ON CONFLICT (kind, entity_id, sha256, consumer) DO UPDATE SET status = EXCLUDED.status, reason = EXCLUDED.reason,
				error = EXCLUDED.error, processed_at = EXCLUDED.processed_at, received_at = EXCLUDED.received_at`,
			k.Kind, k.EntityID, k.SHA256, m.GetConsumer(), status, m.GetReason(), m.GetError(), processedAt, now); err != nil {
			return err
		}
		if status != "failed_retryable" || attempt >= MaxAttempt || deleted != nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE media SET attempt = attempt + 1 WHERE kind = $1 AND entity_id = $2 AND sha256 = $3`,
			k.Kind, k.EntityID, k.SHA256); err != nil {
			return err
		}
		_, err = ev.MediaEvent(ctx, tx, outbox.KindMediaReady, k)
		return err
	})
	if err != nil {
		return fmt.Errorf("handoff: acknowledgement of %s: %w", m.GetMediaId(), err)
	}
	h.add("yt_media_acks_total", "Acknowledgements received, by consumer and status.", map[string]string{"consumer": m.GetConsumer(), "status": status})
	return nil
}

// DeleteDue deletes up to limit files that every required consumer has
// acknowledged (processed, skipped, or failed for good), or whose deadline
// passed, and records content.media.deleted for each. The rows stay. It
// returns how many files went.
func (h *Handoff) DeleteDue(ctx context.Context, limit int) (int, error) {
	now := h.now()
	rows, err := h.Pool.Query(ctx, `SELECT kind, entity_id, sha256, object_key, acked FROM (
			SELECT m.kind, m.entity_id, m.sha256, m.object_key, m.delete_after,
				NOT EXISTS (SELECT 1 FROM unnest($2::text[]) AS c (name) WHERE NOT EXISTS (
					SELECT 1 FROM media_acks a WHERE a.kind = m.kind AND a.entity_id = m.entity_id AND a.sha256 = m.sha256
						AND a.consumer = c.name AND a.status IN ('processed', 'failed_permanent', 'skipped'))) AS acked
			FROM media m WHERE m.announced_at IS NOT NULL AND m.deleted_at IS NULL) x
		WHERE acked OR delete_after <= $1 ORDER BY delete_after LIMIT $3`, now, h.Consumers, limit)
	if err != nil {
		return 0, fmt.Errorf("handoff: files due: %w", err)
	}
	type due struct {
		key    outbox.MediaKey
		object string
		acked  bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.key.Kind, &d.key.EntityID, &d.key.SHA256, &d.object, &d.acked)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range list {
		if err := h.Store.Delete(ctx, d.object); err != nil {
			h.Log.Warn("deleting an analysed file failed; trying again later", "media", d.key.ID(), "err", err)
			h.add("yt_media_delete_failures_total", "Files the hand-off could not delete from storage (tried again).", nil)
			continue
		}
		reason := "expired"
		if d.acked {
			reason = "processed"
		}
		ev := outbox.New(ctx, now)
		if err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE media SET deleted_at = $4, delete_reason = $5
				WHERE kind = $1 AND entity_id = $2 AND sha256 = $3 AND deleted_at IS NULL`,
				d.key.Kind, d.key.EntityID, d.key.SHA256, now, reason)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			_, err = ev.MediaEvent(ctx, tx, outbox.KindMediaDeleted, d.key)
			return err
		}); err != nil {
			return n, fmt.Errorf("handoff: recording a deletion: %w", err)
		}
		h.add("yt_media_deleted_total", "Files deleted after the hand-off, by reason (processed, expired).", map[string]string{"reason": reason})
		n++
	}
	return n, nil
}

// Consume reads acknowledgements until ctx ends or one can't be recorded
// (then it returns the error without committing, and the caller starts a
// new client, which reads from the last commit). Offsets are committed
// after each batch is recorded.
func (h *Handoff) Consume(ctx context.Context, cl *kgo.Client) error {
	for ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		fetches.EachError(func(topic string, p int32, err error) {
			h.Log.Warn("reading acknowledgements", "topic", topic, "partition", p, "err", err)
		})
		var procErr error
		fetches.EachRecord(func(r *kgo.Record) {
			if procErr != nil {
				return
			}
			var m mediav1.MediaProcessed
			if err := proto.Unmarshal(r.Value, &m); err != nil {
				h.Log.Warn("an acknowledgement that isn't a MediaProcessed: skipped", "offset", r.Offset, "err", err)
				return
			}
			procErr = h.Process(ctx, &m)
		})
		if procErr != nil {
			return procErr
		}
		if err := cl.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			return fmt.Errorf("handoff: committing: %w", err)
		}
	}
	return nil
}

// Run announces every minute, deletes what is due every 10 minutes, and
// reads acknowledgements with clients from newClient, until ctx ends.
func (h *Handoff) Run(ctx context.Context, newClient func() (*kgo.Client, error)) {
	go func() {
		for ctx.Err() == nil {
			cl, err := newClient()
			if err == nil {
				err = h.Consume(ctx, cl)
				cl.Close()
			}
			if err != nil && ctx.Err() == nil {
				h.Log.Warn("reading media acknowledgements failed; starting again", "err", err)
				sleep(ctx, 30*time.Second)
			}
		}
	}()
	announce, del := time.NewTicker(time.Minute), time.NewTicker(10*time.Minute)
	defer announce.Stop()
	defer del.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-announce.C:
			if n, err := h.Announce(ctx, 500); err != nil && ctx.Err() == nil {
				h.Log.Warn("announcing images failed", "err", err)
			} else if n > 0 {
				h.Log.Info("images announced to analysis", "files", n)
			}
		case <-del.C:
			if n, err := h.DeleteDue(ctx, 500); err != nil && ctx.Err() == nil {
				h.Log.Warn("deleting analysed images failed", "err", err)
			} else if n > 0 {
				h.Log.Info("analysed or expired images deleted", "files", n)
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
