// Package queue is the job queue: the schedule table holds one row per
// thing to check, with its priority and next due time (plan §8.2). Workers
// lease due rows; a lease expires on its own if a worker dies, so the job
// simply runs again. "Check now" (a new upload seen in a feed) moves a row's
// due time to now and raises its priority.
//
// One job per channel at a time (§8.8): a row that names a channel is not
// leased while another row of the same channel is.
package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
)

// Priorities (plan §8.2).
const (
	P0 = 0 // new uploads and new-video details
	P1 = 1 // new comments
	P2 = 2 // refreshes and sweeps
	P3 = 3 // backfill and candidate checks
	P4 = 4 // commenter enrichment
)

// Job is one leased row.
type Job struct {
	Kind      string
	EntityID  string
	ChannelID string
	Priority  int
	DueAt     time.Time
	Attempts  int // including this run
}

func (j Job) String() string { return j.Kind + "/" + j.EntityID }

// Put adds a row, or moves an existing one earlier: the due time and the
// priority only ever move forward (a refresh never delays a check-now). For
// a row that is running right now, the earlier time is kept in rearm_at and
// applied when the run ends.
func Put(ctx context.Context, q db.Querier, kind, id, channelID string, prio int, due time.Time) error {
	if kind == "" || id == "" {
		return errors.New("queue: kind and ID are required")
	}
	if prio < P0 || prio > P4 {
		return fmt.Errorf("queue: priority %d out of range", prio)
	}
	_, err := q.Exec(ctx, `INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5)
		ON CONFLICT (kind, entity_id) DO UPDATE SET
			due_at = CASE WHEN schedule.lease_until >= now() THEN schedule.due_at
			              ELSE LEAST(schedule.due_at, EXCLUDED.due_at) END,
			rearm_at = CASE WHEN schedule.lease_until >= now()
			                THEN LEAST(COALESCE(schedule.rearm_at, EXCLUDED.due_at), EXCLUDED.due_at)
			                ELSE schedule.rearm_at END,
			priority = LEAST(schedule.priority, EXCLUDED.priority),
			channel_id = COALESCE(EXCLUDED.channel_id, schedule.channel_id)`,
		kind, id, channelID, prio, due)
	return err
}

// Ensure adds a row only if there is none (an existing row keeps its time).
func Ensure(ctx context.Context, q db.Querier, kind, id, channelID string, prio int, due time.Time) (bool, error) {
	tag, err := q.Exec(ctx, `INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5) ON CONFLICT (kind, entity_id) DO NOTHING`, kind, id, channelID, prio, due)
	return tag.RowsAffected() == 1, err
}

// Now is Put with the due time set to now.
func Now(ctx context.Context, q db.Querier, kind, id, channelID string, prio int) error {
	return Put(ctx, q, kind, id, channelID, prio, time.Now())
}

// Lease takes up to n due jobs of the given kinds (all kinds when empty),
// highest priority and oldest due first, for the lease duration.
func Lease(ctx context.Context, pool *pgxpool.Pool, owner string, kinds []string, n int, lease time.Duration) ([]Job, error) {
	if n <= 0 {
		return nil, nil
	}
	var out []Job
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Over-select, then keep at most one job per channel: a channel
		// already leased elsewhere, or taken by another worker's
		// transaction right now (its advisory lock), is skipped.
		rows, err := tx.Query(ctx, `SELECT kind, entity_id, COALESCE(channel_id, ''), priority, due_at, attempts
			FROM schedule s
			WHERE due_at <= now() AND dead_at IS NULL
			  AND (lease_until IS NULL OR lease_until < now())
			  AND ($1::text[] IS NULL OR cardinality($1::text[]) = 0 OR kind = ANY($1))
			  AND (channel_id IS NULL OR NOT EXISTS (
			        SELECT 1 FROM schedule b WHERE b.channel_id = s.channel_id AND b.lease_until >= now()))
			ORDER BY priority, due_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED`, kinds, n*4)
		if err != nil {
			return err
		}
		var cand []Job
		for rows.Next() {
			var j Job
			if err := rows.Scan(&j.Kind, &j.EntityID, &j.ChannelID, &j.Priority, &j.DueAt, &j.Attempts); err != nil {
				rows.Close()
				return err
			}
			cand = append(cand, j)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, j := range cand {
			if len(out) == n {
				break
			}
			if j.ChannelID != "" {
				if taken[j.ChannelID] {
					continue
				}
				var got bool
				if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('queue-channel:' || $1))`, j.ChannelID).Scan(&got); err != nil {
					return err
				}
				if !got {
					continue
				}
				taken[j.ChannelID] = true
			}
			j.Attempts++
			if _, err := tx.Exec(ctx, `UPDATE schedule SET lease_until = now() + make_interval(secs => $3), lease_owner = $4,
				attempts = attempts + 1, last_run = now() WHERE kind = $1 AND entity_id = $2`,
				j.Kind, j.EntityID, lease.Seconds(), owner); err != nil {
				return err
			}
			out = append(out, j)
		}
		return nil
	})
	return out, err
}

// Done releases a job and schedules its next run (zero next: remove it, the
// job is finished for good). A check-now that arrived during the run wins.
func Done(ctx context.Context, q db.Querier, j Job, next time.Time) error {
	if next.IsZero() {
		_, err := q.Exec(ctx, `DELETE FROM schedule WHERE kind = $1 AND entity_id = $2 AND rearm_at IS NULL`, j.Kind, j.EntityID)
		if err != nil {
			return err
		}
		next = time.Now() // a check-now arrived: run once more
	}
	_, err := q.Exec(ctx, `UPDATE schedule SET lease_until = NULL, lease_owner = NULL, attempts = 0, failures = 0,
		last_ok = now(), last_error = NULL, due_at = LEAST($3, COALESCE(rearm_at, $3)), rearm_at = NULL
		WHERE kind = $1 AND entity_id = $2`, j.Kind, j.EntityID, next)
	return err
}

// Fail releases a job after an error and retries it later: 2, 4, 8 … minutes
// by attempt, at most 6 hours. A check-now that arrived meanwhile wins.
//
// itemFailure says the item itself failed (a page that won't parse, an
// error YouTube returns for this item), as opposed to a block or a pool
// with no free IP. Only item failures count towards the job kind's limit
// (MaxFailures); at the limit the job is dead: it keeps its row and reason,
// and is never leased again until Requeue. It reports whether the job died.
func Fail(ctx context.Context, q db.Querier, j Job, cause error, itemFailure bool) (dead bool, err error) {
	add := 0
	if itemFailure {
		add = 1
	}
	err = q.QueryRow(ctx, `UPDATE schedule SET lease_until = NULL, lease_owner = NULL, last_error = left($3, 500),
		failures = failures + $5,
		dead_at = CASE WHEN failures + $5 >= $6 THEN now() ELSE NULL END,
		dead_reason = CASE WHEN failures + $5 >= $6 THEN left($3, 500) ELSE NULL END,
		due_at = LEAST(now() + make_interval(secs => $4), COALESCE(rearm_at, now() + make_interval(secs => $4))), rearm_at = NULL
		WHERE kind = $1 AND entity_id = $2
		RETURNING dead_at IS NOT NULL`, j.Kind, j.EntityID, cause.Error(), Backoff(j.Attempts).Seconds(), add,
		MaxFailures(j.Kind)).Scan(&dead)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // removed while it ran (an erasure, a channel taken off the list)
	}
	return dead, err
}

// DefaultMaxFailures is how many item failures in a row a job may have
// before it is dead. With the backoff that is at least a day of retries.
const DefaultMaxFailures = 10

// maxFailures holds the kinds with a different limit: one-off work that is
// cheap to lose and easy to redo.
var maxFailures = map[string]int{
	KindMedia:     5,
	KindEnrich:    5,
	KindCandidate: 6,
	KindResolve:   6,
}

// MaxFailures is the limit for a job kind.
func MaxFailures(kind string) int {
	if n, ok := maxFailures[kind]; ok {
		return n
	}
	return DefaultMaxFailures
}

// Dead is a summary of dead jobs: how many of a kind died with one reason.
type Dead struct {
	Kind   string
	Reason string
	Count  int64
	Oldest time.Time
}

// DeadJobs summarises the dead jobs by kind and reason (no entity IDs).
func DeadJobs(ctx context.Context, q db.Querier) ([]Dead, error) {
	rows, err := q.Query(ctx, `SELECT kind, COALESCE(left(dead_reason, 120), ''), count(*), min(dead_at)
		FROM schedule WHERE dead_at IS NOT NULL GROUP BY 1, 2 ORDER BY 3 DESC, 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dead
	for rows.Next() {
		var d Dead
		if err := rows.Scan(&d.Kind, &d.Reason, &d.Count, &d.Oldest); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Requeue brings dead jobs back: due now, with their counters reset. kind
// and entityID narrow it ("" means any); it returns how many came back.
func Requeue(ctx context.Context, q db.Querier, kind, entityID string) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE schedule SET dead_at = NULL, dead_reason = NULL, failures = 0, attempts = 0,
		last_error = NULL, due_at = now()
		WHERE dead_at IS NOT NULL AND ($1 = '' OR kind = $1) AND ($2 = '' OR entity_id = $2)`, kind, entityID)
	return tag.RowsAffected(), err
}

// Extend keeps a running job's lease alive (long backfills renew it while
// they work).
func Extend(ctx context.Context, q db.Querier, j Job, lease time.Duration) error {
	_, err := q.Exec(ctx, `UPDATE schedule SET lease_until = now() + make_interval(secs => $3)
		WHERE kind = $1 AND entity_id = $2 AND lease_until IS NOT NULL`, j.Kind, j.EntityID, lease.Seconds())
	return err
}

// Release hands a job back unrun (the worker is shutting down): it is due
// again at once and the attempt does not count.
func Release(ctx context.Context, q db.Querier, j Job) error {
	_, err := q.Exec(ctx, `UPDATE schedule SET lease_until = NULL, lease_owner = NULL,
		attempts = GREATEST(attempts - 1, 0), due_at = LEAST(due_at, COALESCE(rearm_at, due_at)), rearm_at = NULL
		WHERE kind = $1 AND entity_id = $2`, j.Kind, j.EntityID)
	return err
}

// ReleaseOwner hands back every job the given worker still holds, the way
// Release does: a stopping worker calls it last, so nothing it leased waits
// for its lease to expire. It returns how many jobs it handed back.
func ReleaseOwner(ctx context.Context, q db.Querier, owner string) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE schedule SET lease_until = NULL, lease_owner = NULL,
		attempts = GREATEST(attempts - 1, 0), due_at = LEAST(due_at, COALESCE(rearm_at, due_at)), rearm_at = NULL
		WHERE lease_owner = $1 AND lease_until IS NOT NULL`, owner)
	return tag.RowsAffected(), err
}

// Backoff is the retry delay after the given number of failed attempts.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := 2 * time.Minute
	for i := 1; i < attempts && d < 6*time.Hour; i++ {
		d *= 2
	}
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	return d
}

// Remove deletes a row (for example when a channel leaves the list).
func Remove(ctx context.Context, q db.Querier, kind, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM schedule WHERE kind = $1 AND entity_id = $2`, kind, id)
	return err
}

// RemoveChannel deletes every row of a channel.
func RemoveChannel(ctx context.Context, q db.Querier, channelID string) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM schedule WHERE channel_id = $1`, channelID)
	return tag.RowsAffected(), err
}

// Depth is how many rows each kind and priority has, and how many are due.
type Depth struct {
	Kind     string
	Priority int
	Total    int64
	Due      int64
	Leased   int64
	Failing  int64 // last run ended in an error (dead jobs not counted)
	Dead     int64 // too many failures: not retried until requeued
	Oldest   time.Duration
}

// Stats summarises the queue for the admin "health" command and metrics.
func Stats(ctx context.Context, q db.Querier) ([]Depth, error) {
	rows, err := q.Query(ctx, `SELECT kind, priority, count(*),
			count(*) FILTER (WHERE due_at <= now() AND dead_at IS NULL AND (lease_until IS NULL OR lease_until < now())),
			count(*) FILTER (WHERE lease_until >= now()),
			count(*) FILTER (WHERE last_error IS NOT NULL AND dead_at IS NULL),
			count(*) FILTER (WHERE dead_at IS NOT NULL),
			COALESCE(EXTRACT(EPOCH FROM now() - min(due_at) FILTER (WHERE due_at <= now() AND dead_at IS NULL)), 0)
		FROM schedule GROUP BY kind, priority ORDER BY priority, kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Depth
	for rows.Next() {
		var d Depth
		var secs float64
		if err := rows.Scan(&d.Kind, &d.Priority, &d.Total, &d.Due, &d.Leased, &d.Failing, &d.Dead, &secs); err != nil {
			return nil, err
		}
		d.Oldest = time.Duration(secs * float64(time.Second))
		out = append(out, d)
	}
	return out, rows.Err()
}
