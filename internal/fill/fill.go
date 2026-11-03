// Package fill tracks field fill rates (plan §8.11): for each endpoint and
// field, how often the field was present in what the parsers read. A drop
// against the usual level means YouTube changed a page layout; the monitor
// raises an alert before bad data piles up.
package fill

import (
	"context"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
)

type key struct{ endpoint, field string }

type count struct{ seen, present int64 }

// Recorder counts in memory and flushes to the database hourly windows.
type Recorder struct {
	mu sync.Mutex
	c  map[key]*count
}

// Observe records one reading of a field.
func (r *Recorder) Observe(endpoint, field string, present bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c == nil {
		r.c = map[key]*count{}
	}
	k := key{endpoint, field}
	c := r.c[k]
	if c == nil {
		c = &count{}
		r.c[k] = c
	}
	c.seen++
	if present {
		c.present++
	}
}

// Flush adds the counts to the current hour's row and starts again.
func (r *Recorder) Flush(ctx context.Context, q db.Querier, now time.Time) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	c := r.c
	r.c = map[key]*count{}
	r.mu.Unlock()
	window := now.UTC().Truncate(time.Hour)
	for k, v := range c {
		if _, err := q.Exec(ctx, `INSERT INTO field_fill_rates (window_start, endpoint, field, seen, present) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (window_start, endpoint, field) DO UPDATE SET seen = field_fill_rates.seen + EXCLUDED.seen,
				present = field_fill_rates.present + EXCLUDED.present`, window, k.endpoint, k.field, v.seen, v.present); err != nil {
			return err
		}
	}
	return nil
}
