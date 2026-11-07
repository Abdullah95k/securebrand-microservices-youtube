// Package relay publishes yts's outbox to Kafka (REQ-010 fix 12): each
// event's content.* messages (the unified contract, checked) and its yt.*
// message, in one Kafka transaction per batch, in the outbox's order, then
// marks the events sent. One relay sends at a time (a Postgres advisory
// lock), so a post's messages keep their order.
//
// Delivery is at least once: if the relay stops between Kafka's commit and
// Postgres's update, the batch goes again with the same event IDs, which
// consumers drop. An event the contract refuses, or Kafka can't take (too
// large), is not sent: it stays in the outbox with its reason, is counted
// and alerted, and is tried again when the relay restarts after a fix.
//
// The relay never creates a topic (the platform owns them) and registers
// nothing.
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	contracts "github.com/abdullah95k/securebrand-contracts"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/contract"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytevents"
)

// Producer is the part of the Kafka client the relay uses (*kgo.Client).
type Producer interface {
	BeginTransaction() error
	Produce(ctx context.Context, r *kgo.Record, promise func(*kgo.Record, error))
	Flush(ctx context.Context) error
	EndTransaction(ctx context.Context, commit kgo.TransactionEndTry) error
}

// Relay sends the outbox.
type Relay struct {
	Pool     *pgxpool.Pool
	Producer Producer
	Content  *contract.Mapper
	YT       ytevents.Mapper
	Batch    int           // events per transaction (default 500)
	Idle     time.Duration // wait when nothing is pending (default 2s)
	Log      *slog.Logger
	Metrics  *metrics.Registry // nil: none
	Now      func() time.Time
}

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Relay) batch() int {
	if r.Batch <= 0 {
		return 500
	}
	return r.Batch
}

// lockKey is the advisory lock only one relay holds ("ytsrelay").
const lockKey int64 = 0x79747372656c6179

// Result is one batch's outcome.
type Result struct {
	Sent     int // events published
	Messages int // Kafka records
	Refused  int // events that can't be sent
}

// Run sends until ctx ends. It waits while another relay holds the lock,
// clears earlier refusals once it has it (a fix may have come with this
// version), then sends batch after batch, backing off after an error.
func (r *Relay) Run(ctx context.Context) error {
	conn, err := r.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	for {
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil {
			return err
		}
		if got {
			break
		}
		r.Log.Info("another relay is sending; waiting")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
		}
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey) }()
	if n, err := outbox.RetryFailed(ctx, r.Pool); err != nil {
		return err
	} else if n > 0 {
		r.Log.Info("trying refused events again", "events", n)
	}
	r.set("yt_relay_up", "1 while this relay holds the lock and sends.", nil, 1)
	defer r.set("yt_relay_up", "1 while this relay holds the lock and sends.", nil, 0)
	backoff := time.Second
	for ctx.Err() == nil {
		res, err := r.Once(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			r.Log.Warn("sending a batch failed; trying again", "err", err, "in", backoff)
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff = min(2*backoff, time.Minute)
		case res.Sent == 0 && res.Refused == 0:
			backoff = time.Second
			if !sleep(ctx, r.idle()) {
				return nil
			}
		default:
			backoff = time.Second
		}
	}
	return nil
}

func (r *Relay) idle() time.Duration {
	if r.Idle <= 0 {
		return 2 * time.Second
	}
	return r.Idle
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// pending is one event's records.
type pending struct {
	row  outbox.Row
	recs []*kgo.Record
}

// Once sends one batch.
func (r *Relay) Once(ctx context.Context) (Result, error) {
	var res Result
	rows, err := outbox.Pending(ctx, r.Pool, r.batch())
	if err != nil || len(rows) == 0 {
		return res, err
	}
	var batch []pending
	for _, row := range rows {
		recs, err := r.records(row)
		if err != nil {
			reason := "cannot be mapped"
			var rej *contract.Rejected
			if errors.As(err, &rej) {
				reason = "refused by the contract"
			}
			r.Log.Warn("event not sent", "event", row.ID, "kind", row.Kind, "entity", row.EntityID, "why", reason, "err", err)
			if err := outbox.MarkFailed(ctx, r.Pool, row.ID, err.Error()); err != nil {
				return res, err
			}
			r.add("yt_relay_refused_total", "Outbox events not sent: refused by the contract or not mappable, by kind.",
				map[string]string{"kind": row.Kind}, 1)
			res.Refused++
			continue
		}
		batch = append(batch, pending{row, recs})
	}
	if len(batch) == 0 {
		return res, nil
	}
	if err := r.Producer.BeginTransaction(); err != nil {
		return res, fmt.Errorf("relay: beginning a transaction: %w", err)
	}
	var mu sync.Mutex
	failed := map[int64]error{}
	var wg sync.WaitGroup
	for _, p := range batch {
		for _, rec := range p.recs {
			wg.Add(1)
			id := p.row.ID
			r.Producer.Produce(ctx, rec, func(_ *kgo.Record, err error) {
				defer wg.Done()
				if err != nil {
					mu.Lock()
					if failed[id] == nil {
						failed[id] = err
					}
					mu.Unlock()
				}
			})
		}
	}
	flushErr := r.Producer.Flush(ctx)
	wg.Wait()
	if flushErr != nil || len(failed) > 0 {
		abortErr := r.Producer.EndTransaction(context.WithoutCancel(ctx), kgo.TryAbort)
		r.add("yt_relay_transactions_total", "Kafka transactions by result.", map[string]string{"result": "aborted"}, 1)
		// An event Kafka will never take is set aside; the rest go again.
		for id, err := range failed {
			if permanent(err) {
				if err := outbox.MarkFailed(ctx, r.Pool, id, "kafka: "+err.Error()); err != nil {
					return res, err
				}
				res.Refused++
				r.add("yt_relay_refused_total", "Outbox events not sent: refused by the contract or not mappable, by kind.",
					map[string]string{"kind": kindOf(batch, id)}, 1)
			}
		}
		return res, fmt.Errorf("relay: batch aborted (%d events failed, flush: %v, abort: %v): %w", len(failed), flushErr, abortErr,
			firstErr(failed, flushErr))
	}
	if err := r.Producer.EndTransaction(ctx, kgo.TryCommit); err != nil {
		r.add("yt_relay_transactions_total", "Kafka transactions by result.", map[string]string{"result": "error"}, 1)
		return res, fmt.Errorf("relay: committing: %w", err)
	}
	r.add("yt_relay_transactions_total", "Kafka transactions by result.", map[string]string{"result": "committed"}, 1)
	ids := make([]int64, 0, len(batch))
	for _, p := range batch {
		ids = append(ids, p.row.ID)
		res.Messages += len(p.recs)
		for _, rec := range p.recs {
			r.add("yt_relay_published_total", "Messages published, by topic.", map[string]string{"topic": rec.Topic}, 1)
		}
	}
	if err := outbox.MarkSent(context.WithoutCancel(ctx), r.Pool, ids, r.now()); err != nil {
		// Sent but not marked: they go again, with the same event IDs.
		return res, fmt.Errorf("relay: marking %d sent events: %w", len(ids), err)
	}
	res.Sent = len(ids)
	r.set("yt_relay_last_commit_timestamp_seconds", "When the relay last committed a batch.", nil, float64(r.now().Unix()))
	return res, nil
}

func kindOf(batch []pending, id int64) string {
	for _, p := range batch {
		if p.row.ID == id {
			return p.row.Kind
		}
	}
	return ""
}

func firstErr(failed map[int64]error, flushErr error) error {
	for _, err := range failed {
		return err
	}
	return flushErr
}

// permanent: Kafka will never take this record as it is.
func permanent(err error) bool {
	return errors.Is(err, kerr.MessageTooLarge) || errors.Is(err, kerr.RecordListTooLarge) ||
		errors.Is(err, kerr.InvalidRecord) || errors.Is(err, kerr.CorruptMessage)
}

// records are an event's Kafka records: its content.* messages (checked)
// and its yt.* message.
func (r *Relay) records(row outbox.Row) ([]*kgo.Record, error) {
	var recs []*kgo.Record
	msgs, err := r.Content.Map(row)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		rec, err := record(m.Topic, m.Key, m.Msg, kgo.RecordHeader{Key: "sb-contract", Value: []byte(contracts.Version)})
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	ym, err := r.YT.Map(row)
	if err != nil {
		return nil, err
	}
	if ym != nil {
		rec, err := record(ym.Topic, ym.Key, ym.Msg)
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func record(topic, key string, msg proto.Message, extra ...kgo.RecordHeader) (*kgo.Record, error) {
	v, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("relay: encoding %s: %w", topic, err)
	}
	h := append([]kgo.RecordHeader{{Key: "sb-type", Value: []byte(msg.ProtoReflect().Descriptor().FullName())}}, extra...)
	return &kgo.Record{Topic: topic, Key: []byte(key), Value: v, Headers: h}, nil
}

func (r *Relay) add(name, help string, labels map[string]string, v float64) {
	if r.Metrics != nil {
		r.Metrics.Add(name, help, labels, v)
	}
}

func (r *Relay) set(name, help string, labels map[string]string, v float64) {
	if r.Metrics != nil {
		r.Metrics.Set(name, help, labels, v)
	}
}

// Topics are every topic the relay publishes to, for the readiness check.
func Topics() []string {
	t := []string{contract.TopicAccount, contract.TopicAccountSnapshot, contract.TopicPost, contract.TopicComment,
		contract.TopicMetrics, contract.TopicPlaylist, contract.TopicCandidate, contract.TopicCoverage}
	t = append(t, ytevents.Topics()...)
	return t
}
