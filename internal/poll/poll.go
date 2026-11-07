// Package poll paces loops that poll the database for work (REQ-024): a
// loop that finds nothing waits longer each time, from Min up to Max, and
// starts again from Min as soon as it finds work. An idle pod then touches
// PostgreSQL every Max instead of every few seconds, and PgBouncer can
// give its server connection back between polls.
package poll

import (
	"context"
	"time"
)

// The platform's suggestion: from 1 second up to 15–30 seconds.
const (
	DefaultMin = time.Second
	DefaultMax = 30 * time.Second
)

// Backoff is the wait between polls that found nothing. The zero value
// waits DefaultMin, doubling up to DefaultMax.
type Backoff struct {
	Min, Max time.Duration
	cur      time.Duration
}

func (b *Backoff) bounds() (lo, hi time.Duration) {
	lo, hi = b.Min, b.Max
	if lo <= 0 {
		lo = DefaultMin
	}
	if hi <= 0 {
		hi = DefaultMax
	}
	return min(lo, hi), hi
}

// Next is the wait after a poll that found nothing: Min the first time,
// then twice the last, up to Max.
func (b *Backoff) Next() time.Duration {
	lo, hi := b.bounds()
	if b.cur == 0 {
		b.cur = lo
	} else {
		b.cur = min(2*b.cur, hi)
	}
	return b.cur
}

// Reset starts again from Min (the poll found work).
func (b *Backoff) Reset() { b.cur = 0 }

// Wait waits Next, or until ctx ends; it reports whether ctx is still on.
func (b *Backoff) Wait(ctx context.Context) bool {
	t := time.NewTimer(b.Next())
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
