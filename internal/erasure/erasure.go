// Package erasure carries out erasure requests (plan §8.9) and keeps them
// in force:
//
//   - a request names an author, a channel, a video, a comment or a post;
//   - the target goes on the suppression list first, so every collector
//     drops new data about it from that moment;
//   - then everything stored about it is deleted, each step logged in
//     erasure_audit with row counts only (no personal data), and the
//     request's raw target is cleared (only its keyed hash stays);
//   - a daily audit checks that nothing on the suppression list is in the
//     database, and erases again whatever came back (for example after a
//     backup was restored);
//   - optional time-based retention and redaction of deleted comments run
//     only if switched on (they are off: everything is kept, D18, D26).
package erasure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/webhook"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
)

// KindErasure is the job that carries out a request (entity: request ID).
const KindErasure = "erasure"

// Service runs erasures.
type Service struct {
	Pool  *pgxpool.Pool
	Keyer *privacy.Keyer
	Sup   *privacy.Suppressor
	Fetch *phase0.Collector // resolves handles and URLs; nil: IDs only
	Log   *slog.Logger
	Media media.Store // stored images; nil: no storage configured
	// Webhooks: tell the receiving systems about each erasure (an
	// "erasure" event; its ID is removed once delivered).
	Webhooks bool
}

// Handlers maps the erasure job kind to its handler.
func (s *Service) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{KindErasure: s.run}
}

// Request records an erasure request and queues it at P0. target is a
// YouTube ID, a channel URL or a @handle (authors and channels).
func (s *Service) Request(ctx context.Context, kind, target, requestedBy string) (int64, error) {
	target = strings.TrimSpace(target)
	if !privacy.ValidKind(kind) {
		return 0, fmt.Errorf("erasure: kind %q is not one of %s", kind, strings.Join(privacy.Kinds, ", "))
	}
	if target == "" {
		return 0, errors.New("erasure: no target")
	}
	var id int64
	err := s.Pool.QueryRow(ctx, `INSERT INTO erasure_requests (kind, target, target_key, requested_by) VALUES ($1, $2, $3, NULLIF($4, ''))
		RETURNING id`, kind, target, s.Keyer.Key(kind, target), requestedBy).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := queue.Now(ctx, s.Pool, KindErasure, fmt.Sprint(id), "", queue.P0); err != nil {
		return id, err
	}
	return id, nil
}

// resolve turns a target into a YouTube ID.
func (s *Service) resolve(ctx context.Context, kind, target string) (string, error) {
	t := strings.TrimSpace(target)
	if kind == privacy.Author || kind == privacy.Channel {
		if i := strings.Index(t, "/channel/"); i >= 0 {
			t = t[i+len("/channel/"):]
			if j := strings.IndexAny(t, "/?#"); j >= 0 {
				t = t[:j]
			}
		}
		if strings.HasPrefix(t, "UC") && len(t) == 24 {
			return t, nil
		}
		// A handle: what we stored first, then YouTube.
		h := t
		if i := strings.LastIndex(h, "/@"); i >= 0 {
			h = h[i+1:]
		}
		if strings.HasPrefix(h, "@") {
			var id string
			err := s.Pool.QueryRow(ctx, `SELECT id FROM (
					SELECT id FROM channels WHERE lower(handle) = lower($1)
					UNION ALL SELECT id FROM authors WHERE lower(handle) = lower($1)
					UNION ALL SELECT DISTINCT author_id FROM comments WHERE lower(author_handle) = lower($1) AND author_id IS NOT NULL
				) x LIMIT 1`, h).Scan(&id)
			if err == nil {
				return id, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
		}
		if s.Fetch != nil {
			var cost phase0.Cost
			return s.Fetch.Resolve(ctx, t, &cost)
		}
		return "", fmt.Errorf("erasure: %q is not a channel ID, and no resolver is available", target)
	}
	if kind == privacy.Video {
		if i := strings.Index(t, "v="); i >= 0 {
			t = t[i+2:]
			if j := strings.IndexAny(t, "&#"); j >= 0 {
				t = t[:j]
			}
		} else if i := strings.LastIndex(t, "/"); i >= 0 {
			t = t[i+1:]
		}
	}
	return t, nil
}

func (s *Service) run(ctx context.Context, j queue.Job) (time.Time, error) {
	var kind string
	var target *string
	var status string
	err := s.Pool.QueryRow(ctx, `SELECT kind, target, status FROM erasure_requests WHERE id = $1`, j.EntityID).Scan(&kind, &target, &status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status == "done" {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if target == nil {
		return time.Time{}, errors.New("erasure: the request has no target")
	}
	id, err := s.resolve(ctx, kind, *target)
	if err != nil {
		if j.Attempts >= 5 {
			_, _ = s.Pool.Exec(ctx, `UPDATE erasure_requests SET status = 'failed', error = left($2, 300) WHERE id = $1`, j.EntityID, err.Error())
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var reqID int64
	fmt.Sscan(j.EntityID, &reqID)
	// Suppress first: from here on nothing about the target is stored.
	if err := s.Sup.Add(ctx, s.Pool, kind, id, "erasure request", &reqID); err != nil {
		return time.Time{}, err
	}
	s.audit(ctx, reqID, "suppressed", 1)
	if err := s.Erase(ctx, reqID, kind, id); err != nil {
		return time.Time{}, err
	}
	if s.Webhooks {
		if err := webhook.Emit(ctx, s.Pool, webhook.Erasure, "erasure:"+j.EntityID, "",
			map[string]any{"kind": kind, "id": id, "request_id": reqID}); err != nil {
			return time.Time{}, err
		}
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE erasure_requests SET status = 'done', done_at = now(), target = NULL, error = NULL,
		target_key = $2 WHERE id = $1`, j.EntityID, s.Keyer.Key(kind, id)); err != nil {
		return time.Time{}, err
	}
	s.Log.Info("erasure done", "request", reqID, "kind", kind)
	return time.Time{}, nil
}

func (s *Service) audit(ctx context.Context, reqID int64, step string, rows int64) {
	_, _ = s.Pool.Exec(ctx, `INSERT INTO erasure_audit (request_id, step, rows) VALUES ($1, $2, $3)`, reqID, step, rows)
}

type step struct {
	name string
	sql  string
}

// Erase deletes everything stored about one target, in one transaction,
// logging each step's row count under reqID (0 for audit re-erasures).
func (s *Service) Erase(ctx context.Context, reqID int64, kind, id string) error {
	var steps []step
	switch kind {
	case privacy.Author:
		steps = []step{
			{"outbox events", `DELETE FROM event_outbox WHERE kind = 'author' AND entity_id = $1
				OR kind IN ('comment', 'comment_metrics') AND entity_id IN (SELECT id FROM comments WHERE author_id = $1)`},
			{"outbox state", `DELETE FROM outbox_state WHERE kind = 'author' AND entity_id = $1
				OR kind = 'comment' AND entity_id IN (SELECT id FROM comments WHERE author_id = $1)`},
			{"comment versions", `DELETE FROM comment_versions WHERE comment_id IN (SELECT id FROM comments WHERE author_id = $1)`},
			{"comments", `DELETE FROM comments WHERE author_id = $1`},
			{"author profiles", `DELETE FROM author_profiles WHERE author_id = $1`},
			{"author badges", `DELETE FROM author_badges WHERE author_id = $1`},
			{"graph", `DELETE FROM channel_links_graph WHERE from_channel = $1 AND relation = 'commenter'`},
			{"media", `DELETE FROM media WHERE kind = 'author_avatar' AND entity_id = $1`},
			{"jobs", `DELETE FROM schedule WHERE kind = 'enrich' AND entity_id = $1
				OR kind = 'media' AND entity_id = 'author_avatar:' || $1`},
			{"author", `DELETE FROM authors WHERE id = $1`},
		}
	case privacy.Channel:
		steps = []step{
			// The channel's events, and its videos', posts', playlists' and
			// their comments' (keyed by their post).
			{"outbox events", `DELETE FROM event_outbox WHERE partition_key = $1 OR partition_key IN (
				SELECT id FROM videos WHERE channel_id = $1 UNION ALL SELECT id FROM posts WHERE channel_id = $1
				UNION ALL SELECT id FROM playlists WHERE channel_id = $1)`},
			{"outbox state", `DELETE FROM outbox_state WHERE kind IN ('channel', 'candidate') AND entity_id = $1
				OR kind = 'video' AND entity_id IN (SELECT id FROM videos WHERE channel_id = $1)
				OR kind = 'post' AND entity_id IN (SELECT id FROM posts WHERE channel_id = $1)
				OR kind = 'playlist' AND entity_id IN (SELECT id FROM playlists WHERE channel_id = $1)
				OR kind = 'comment' AND entity_id IN (SELECT id FROM comments WHERE channel_id = $1)`},
			{"comment versions", `DELETE FROM comment_versions WHERE comment_id IN (SELECT id FROM comments WHERE channel_id = $1)`},
			{"comments", `DELETE FROM comments WHERE channel_id = $1`},
			{"video snapshots", `DELETE FROM video_snapshots WHERE video_id IN (SELECT id FROM videos WHERE channel_id = $1)`},
			{"video changes", `DELETE FROM video_changes WHERE video_id IN (SELECT id FROM videos WHERE channel_id = $1)
				OR video_id IN (SELECT id FROM posts WHERE channel_id = $1)`},
			{"playlist items", `DELETE FROM playlist_items WHERE playlist_id IN (SELECT id FROM playlists WHERE channel_id = $1)
				OR video_id IN (SELECT id FROM videos WHERE channel_id = $1)`},
			{"playlists", `DELETE FROM playlists WHERE channel_id = $1`},
			{"search hits", `DELETE FROM search_hits WHERE channel_id = $1`},
			{"media", `DELETE FROM media WHERE kind IN ('avatar', 'banner', 'thumb') AND (entity_id = $1 OR channel_id = $1)`},
			{"webhook events", `DELETE FROM webhook_events WHERE type <> 'erasure' AND (channel_id = $1 OR entity_id = $1)`},
			{"media jobs", `DELETE FROM schedule WHERE kind = 'media' AND (entity_id IN ('avatar:' || $1, 'banner:' || $1)
				OR entity_id IN (SELECT 'thumb:' || id FROM videos WHERE channel_id = $1))`},
			{"videos", `DELETE FROM videos WHERE channel_id = $1`},
			{"post snapshots", `DELETE FROM post_snapshots WHERE post_id IN (SELECT id FROM posts WHERE channel_id = $1)`},
			{"posts", `DELETE FROM posts WHERE channel_id = $1`},
			{"channel snapshots", `DELETE FROM channel_snapshots WHERE channel_id = $1`},
			{"channel changes", `DELETE FROM channel_changes WHERE channel_id = $1`},
			{"graph", `DELETE FROM channel_links_graph WHERE from_channel = $1 OR to_channel = $1`},
			{"candidate", `DELETE FROM candidates WHERE channel_id = $1`},
			{"channel inputs", `DELETE FROM channel_inputs WHERE channel_id = $1`},
			{"jobs", `DELETE FROM schedule WHERE channel_id = $1 OR entity_id = $1`},
			{"channel", `DELETE FROM channels WHERE id = $1`},
		}
	case privacy.Video:
		steps = []step{
			{"outbox events", `DELETE FROM event_outbox WHERE partition_key = $1`}, // the video, its counts, its comments
			{"outbox state", `DELETE FROM outbox_state WHERE kind = 'video' AND entity_id = $1
				OR kind = 'comment' AND entity_id IN (SELECT id FROM comments WHERE parent_kind = 'video' AND parent_id = $1)`},
			{"comment versions", `DELETE FROM comment_versions WHERE comment_id IN (SELECT id FROM comments WHERE parent_kind = 'video' AND parent_id = $1)`},
			{"comments", `DELETE FROM comments WHERE parent_kind = 'video' AND parent_id = $1`},
			{"video snapshots", `DELETE FROM video_snapshots WHERE video_id = $1`},
			{"video changes", `DELETE FROM video_changes WHERE video_id = $1`},
			{"playlist items", `DELETE FROM playlist_items WHERE video_id = $1`},
			{"search hits", `DELETE FROM search_hits WHERE video_id = $1`},
			{"media", `DELETE FROM media WHERE kind = 'thumb' AND entity_id = $1`},
			{"webhook events", `DELETE FROM webhook_events WHERE type <> 'erasure' AND entity_id = $1`},
			{"jobs", `DELETE FROM schedule WHERE entity_id = $1 OR kind = 'media' AND entity_id = 'thumb:' || $1`},
			{"video", `DELETE FROM videos WHERE id = $1`},
		}
	case privacy.Comment:
		steps = []step{
			{"outbox events", `DELETE FROM event_outbox WHERE kind IN ('comment', 'comment_metrics') AND entity_id = $1`},
			{"outbox state", `DELETE FROM outbox_state WHERE kind = 'comment' AND entity_id = $1`},
			{"comment versions", `DELETE FROM comment_versions WHERE comment_id = $1`},
			{"comment", `DELETE FROM comments WHERE id = $1`},
		}
	case privacy.Post:
		steps = []step{
			{"outbox events", `DELETE FROM event_outbox WHERE partition_key = $1`}, // the post, its counts, its comments
			{"outbox state", `DELETE FROM outbox_state WHERE kind = 'post' AND entity_id = $1
				OR kind = 'comment' AND entity_id IN (SELECT id FROM comments WHERE parent_kind = 'post' AND parent_id = $1)`},
			{"comment versions", `DELETE FROM comment_versions WHERE comment_id IN (SELECT id FROM comments WHERE parent_kind = 'post' AND parent_id = $1)`},
			{"comments", `DELETE FROM comments WHERE parent_kind = 'post' AND parent_id = $1`},
			{"post snapshots", `DELETE FROM post_snapshots WHERE post_id = $1`},
			{"post changes", `DELETE FROM video_changes WHERE video_id = $1`},
			{"webhook events", `DELETE FROM webhook_events WHERE type <> 'erasure' AND entity_id = $1`},
			{"jobs", `DELETE FROM schedule WHERE entity_id = $1`},
			{"post", `DELETE FROM posts WHERE id = $1`},
		}
	default:
		return fmt.Errorf("erasure: kind %q", kind)
	}
	// Stored images go first: their rows are deleted with the rest, so a
	// file that could not be deleted keeps its row and the erasure retries.
	keys, err := media.Keys(ctx, s.Pool, kind, id)
	if err != nil {
		return fmt.Errorf("erasure: media: %w", err)
	}
	if len(keys) > 0 && s.Media == nil {
		return fmt.Errorf("erasure: %d stored images, but no storage is configured to delete them from", len(keys))
	}
	for _, k := range keys {
		if err := s.Media.Delete(ctx, k); err != nil {
			return fmt.Errorf("erasure: deleting an image: %w", err)
		}
	}
	counts := map[string]int64{"media files": int64(len(keys))}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, st := range steps {
			tag, err := tx.Exec(ctx, st.sql, id)
			if err != nil {
				return fmt.Errorf("erasure step %q: %w", st.name, err)
			}
			counts[st.name] += tag.RowsAffected()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(keys) > 0 {
		s.audit(ctx, reqID, "media files", counts["media files"])
	}
	for _, st := range steps {
		s.audit(ctx, reqID, st.name, counts[st.name])
	}
	return nil
}

// AuditResult is what the daily audit found.
type AuditResult struct {
	Violations map[string]int // kind → items on the suppression list found in the database
}

// Total is the number of violations.
func (a AuditResult) Total() int {
	n := 0
	for _, v := range a.Violations {
		n += v
	}
	return n
}

// Audit checks every stored ID against the suppression list (plan §8.9:
// "a daily job checks that suppressed items in the database = 0") and
// erases again what it finds.
func (s *Service) Audit(ctx context.Context) (AuditResult, error) {
	res := AuditResult{Violations: map[string]int{}}
	if err := s.Sup.Refresh(ctx, s.Pool); err != nil {
		return res, err
	}
	checks := []struct{ kind, sql string }{
		{privacy.Channel, `SELECT id FROM channels`},
		{privacy.Author, `SELECT id FROM authors UNION SELECT DISTINCT author_id FROM comments WHERE author_id IS NOT NULL`},
		{privacy.Video, `SELECT id FROM videos`},
		{privacy.Post, `SELECT id FROM posts`},
		{privacy.Comment, `SELECT id FROM comments`},
		{privacy.Video, `SELECT entity_id FROM media WHERE kind = 'thumb'`},
		{privacy.Channel, `SELECT entity_id FROM media WHERE kind IN ('avatar', 'banner')
			UNION SELECT channel_id FROM media WHERE channel_id IS NOT NULL`},
		{privacy.Author, `SELECT entity_id FROM media WHERE kind = 'author_avatar'`},
		{privacy.Video, `SELECT entity_id FROM webhook_events WHERE type = 'video.relevant'`},
		{privacy.Post, `SELECT entity_id FROM webhook_events WHERE type = 'post.relevant'`},
		{privacy.Channel, `SELECT channel_id FROM webhook_events WHERE channel_id IS NOT NULL`},
	}
	for _, c := range checks {
		rows, err := s.Pool.Query(ctx, c.sql)
		if err != nil {
			return res, err
		}
		var bad []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return res, err
			}
			if s.Sup.Suppressed(c.kind, id) {
				bad = append(bad, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, err
		}
		for _, id := range bad {
			if err := s.Erase(ctx, 0, c.kind, id); err != nil {
				return res, err
			}
		}
		res.Violations[c.kind] += len(bad)
	}
	if n := res.Total(); n > 0 {
		s.Log.Error("suppressed items were found in the database and erased again", "items", n)
	}
	return res, nil
}

// ApplyRetention runs the optional retention settings and returns the rows
// each step changed. With the defaults (keep forever, no redaction) it
// changes nothing.
func ApplyRetention(ctx context.Context, pool *pgxpool.Pool, r privacy.Retention, now time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	run := func(name, sql string, args ...any) error {
		tag, err := pool.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("retention %s: %w", name, err)
		}
		out[name] += tag.RowsAffected()
		return nil
	}
	if r.CommentsDays > 0 {
		cut := now.AddDate(0, 0, -r.CommentsDays)
		if err := run("comment versions", `DELETE FROM comment_versions WHERE comment_id IN (SELECT id FROM comments WHERE first_seen < $1)`, cut); err != nil {
			return out, err
		}
		if err := run("comments", `DELETE FROM comments WHERE first_seen < $1`, cut); err != nil {
			return out, err
		}
	}
	if r.SnapshotsDays > 0 {
		cut := now.AddDate(0, 0, -r.SnapshotsDays)
		for _, t := range []string{"channel_snapshots", "video_snapshots", "post_snapshots"} {
			if err := run(t, `DELETE FROM `+t+` WHERE taken_at < $1`, cut); err != nil {
				return out, err
			}
		}
	}
	if r.ProfilesDays > 0 {
		if err := run("author profiles", `DELETE FROM author_profiles WHERE fetched_at < $1`, now.AddDate(0, 0, -r.ProfilesDays)); err != nil {
			return out, err
		}
	}
	if r.RedactDeletedComments {
		if err := run("redacted comments", `UPDATE comments SET text = '', author_handle = NULL, author_name = NULL,
			author_avatar_url = NULL, author_badges = NULL WHERE deleted_at IS NOT NULL AND text <> ''`); err != nil {
			return out, err
		}
		if err := run("redacted versions", `UPDATE comment_versions SET text = '' WHERE text <> ''
			AND comment_id IN (SELECT id FROM comments WHERE deleted_at IS NOT NULL)`); err != nil {
			return out, err
		}
	}
	return out, nil
}
