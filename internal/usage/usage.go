// Package usage measures what collection costs (REQ-010 fix 5, standard
// D5): each job carries a Meter in its context, the website client adds
// every request, the bytes on the wire and every block signal to it, and
// the worker records the totals per hour and job kind, and per day and
// channel. "ytcollect report" reads them back (report.go).
package usage

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

// Iraq is the owner's time zone (UTC+3, no daylight saving): days in the
// channel table start at 00:00 Iraq time.
var Iraq = time.FixedZone("Iraq (UTC+3)", 3*3600)

// Meter adds up one job's cost. Safe for concurrent use; a nil Meter
// ignores everything, so callers outside a job need no check.
type Meter struct {
	requests, bytes, blocks atomic.Int64
}

type meterKey struct{}

// With returns a context carrying a new Meter, and the Meter.
func With(ctx context.Context) (context.Context, *Meter) {
	m := &Meter{}
	return context.WithValue(ctx, meterKey{}, m), m
}

// From returns the context's Meter, or nil.
func From(ctx context.Context) *Meter {
	m, _ := ctx.Value(meterKey{}).(*Meter)
	return m
}

// Request counts one HTTP request sent and the bytes it moved.
func (m *Meter) Request(bytes int64) {
	if m == nil {
		return
	}
	m.requests.Add(1)
	m.bytes.Add(max(bytes, 0))
}

// Block counts one block signal.
func (m *Meter) Block() {
	if m != nil {
		m.blocks.Add(1)
	}
}

// Totals are a Meter's counts.
type Totals struct {
	Requests, Bytes, Blocks int64
}

// Totals reads the counts so far.
func (m *Meter) Totals() Totals {
	if m == nil {
		return Totals{}
	}
	return Totals{Requests: m.requests.Load(), Bytes: m.bytes.Load(), Blocks: m.blocks.Load()}
}

// PerChannel says whether a job kind's cost belongs to the job's channel.
// Candidate checks name the channel being checked, which isn't monitored
// yet, so they count as discovery; the rest have no channel.
func PerChannel(kind string) bool {
	switch kind {
	case queue.KindCandidate, queue.KindSearch, queue.KindEnrich, queue.KindMedia, queue.KindResolve:
		return false
	}
	return true
}

// Record adds one finished job to the hourly and daily totals, in one
// statement. at is when the job started.
func Record(ctx context.Context, q db.Querier, at time.Time, j queue.Job, t Totals, failed bool, took time.Duration) error {
	fail := 0
	if failed {
		fail = 1
	}
	channel := ""
	if PerChannel(j.Kind) {
		channel = j.ChannelID
	}
	var bfReq, bfBytes int64
	if j.Kind == queue.KindBackfill {
		bfReq, bfBytes = t.Requests, t.Bytes
	}
	_, err := q.Exec(ctx, `WITH h AS (
			INSERT INTO usage_hourly (hour, kind, jobs, failures, requests, bytes, blocks, run_ms)
			VALUES (date_trunc('hour', $1::timestamptz), $2, 1, $3, $4, $5, $6, $7)
			ON CONFLICT (hour, kind) DO UPDATE SET jobs = usage_hourly.jobs + 1,
				failures = usage_hourly.failures + EXCLUDED.failures, requests = usage_hourly.requests + EXCLUDED.requests,
				bytes = usage_hourly.bytes + EXCLUDED.bytes, blocks = usage_hourly.blocks + EXCLUDED.blocks,
				run_ms = usage_hourly.run_ms + EXCLUDED.run_ms)
		INSERT INTO usage_channel_daily (day, channel_id, jobs, failures, requests, bytes, blocks, run_ms, backfill_requests, backfill_bytes)
		SELECT $8::date, $9, 1, $3, $4, $5, $6, $7, $10, $11 WHERE $9 <> ''
		ON CONFLICT (day, channel_id) DO UPDATE SET jobs = usage_channel_daily.jobs + 1,
			failures = usage_channel_daily.failures + EXCLUDED.failures, requests = usage_channel_daily.requests + EXCLUDED.requests,
			bytes = usage_channel_daily.bytes + EXCLUDED.bytes, blocks = usage_channel_daily.blocks + EXCLUDED.blocks,
			run_ms = usage_channel_daily.run_ms + EXCLUDED.run_ms,
			backfill_requests = usage_channel_daily.backfill_requests + EXCLUDED.backfill_requests,
			backfill_bytes = usage_channel_daily.backfill_bytes + EXCLUDED.backfill_bytes`,
		at.UTC(), j.Kind, fail, t.Requests, t.Bytes, t.Blocks, took.Milliseconds(),
		Day(at), channel, bfReq, bfBytes)
	return err
}

// Day is the Iraq-time date of t, as the channel table keys it.
func Day(t time.Time) string { return t.In(Iraq).Format(time.DateOnly) }

// SampleSize records the database's size and its largest tables (the
// partitions of a table counted under the table). The scheduler runs it
// hourly.
func SampleSize(ctx context.Context, q db.Querier, now time.Time) error {
	var total int64
	if err := q.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&total); err != nil {
		return err
	}
	rows, err := q.Query(ctx, `SELECT COALESCE(p.relname, c.relname), sum(pg_total_relation_size(c.oid))::bigint
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_inherits i ON i.inhrelid = c.oid LEFT JOIN pg_class p ON p.oid = i.inhparent
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'm')
		GROUP BY 1 ORDER BY 2 DESC LIMIT 20`)
	if err != nil {
		return err
	}
	tables := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			rows.Close()
			return err
		}
		tables[name] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	raw, _ := json.Marshal(tables)
	_, err = q.Exec(ctx, `INSERT INTO db_sizes (at, bytes, tables) VALUES ($1, $2, $3) ON CONFLICT (at) DO NOTHING`,
		now.UTC().Truncate(time.Second), total, raw)
	return err
}
