// Package lease runs work that only one process may do at a time (the
// relay), through a row in the leases table rather than a session advisory
// lock, which doesn't work through PgBouncer's transaction pooling
// (REQ-023): the holder renews the row every 5 seconds in a short
// statement; the lease lasts 15 seconds unless renewed; a standby tries to
// take it every 15 seconds (REQ-024). The work stops as soon as a renewal
// fails or finds the lease gone.
package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
)

// The platform's rule (REQ-023, REQ-024).
const (
	DefaultTTL   = 15 * time.Second // a lease ends this long after its last renewal
	DefaultRenew = 5 * time.Second  // the holder renews this often
	callTimeout  = 3 * time.Second  // each renewal's own limit
)

// Lease is one named lease and this process's claim on it.
type Lease struct {
	Q      db.Querier
	Name   string
	Holder string        // this process; Holder() when empty
	TTL    time.Duration // DefaultTTL when zero
	Renew  time.Duration // DefaultRenew when zero
	Log    *slog.Logger
	// OnChange, when set, is told when this process takes the lease (true)
	// and loses or gives it up (false).
	OnChange func(held bool)

	once sync.Once
}

// Holder names this process: its host, process ID and a random part.
func Holder() string {
	host, _ := os.Hostname()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b))
}

func (l *Lease) init() {
	l.once.Do(func() {
		if l.Holder == "" {
			l.Holder = Holder()
		}
		if l.TTL <= 0 {
			l.TTL = DefaultTTL
		}
		if l.Renew <= 0 {
			l.Renew = DefaultRenew
		}
		if l.Log == nil {
			l.Log = slog.Default()
		}
	})
}

// Claim takes the lease, or renews it, for TTL from now. It reports
// whether this process holds it: false when another process does.
func (l *Lease) Claim(ctx context.Context) (bool, error) {
	l.init()
	var until time.Time
	err := l.Q.QueryRow(ctx, `INSERT INTO leases AS l (name, holder, until) VALUES ($1, $2, now() + make_interval(secs => $3::double precision))
		ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, until = EXCLUDED.until
		WHERE l.holder = EXCLUDED.holder OR l.until < now()
		RETURNING until`, l.Name, l.Holder, l.TTL.Seconds()).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Release gives the lease up, if this process holds it, so a standby can
// take it at once.
func (l *Lease) Release(ctx context.Context) error {
	l.init()
	_, err := l.Q.Exec(ctx, `UPDATE leases SET until = now() - interval '1 second' WHERE name = $1 AND holder = $2`, l.Name, l.Holder)
	return err
}

func (l *Lease) changed(held bool) {
	if l.OnChange != nil {
		l.OnChange(held)
	}
}

// Run does work while this process holds the lease, until ctx ends or work
// returns. Without the lease it waits, trying every TTL. Holding it, it
// renews every Renew; when a renewal fails or finds the lease taken, it
// cancels work's context, waits for work to return, and stands by again.
// Run returns work's error when work returns on its own, and nil when ctx
// ends; either way it gives the lease up.
func (l *Lease) Run(ctx context.Context, work func(ctx context.Context) error) error {
	l.init()
	waiting := false
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		held, err := l.Claim(cctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			l.Log.Warn("taking the lease failed; trying again", "lease", l.Name, "err", err, "in", l.TTL.String())
		}
		if !held {
			if !waiting && err == nil {
				l.Log.Info("another process holds the lease; standing by", "lease", l.Name)
				waiting = true
			}
			if !sleep(ctx, l.TTL) {
				return nil
			}
			continue
		}
		waiting = false
		l.Log.Info("lease taken", "lease", l.Name, "holder", l.Holder)
		l.changed(true)
		lost, err := l.hold(ctx, work)
		l.changed(false)
		if !lost || ctx.Err() != nil {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
			if rerr := l.Release(rctx); rerr != nil {
				l.Log.Warn("giving the lease up failed; it ends on its own", "lease", l.Name, "err", rerr)
			}
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		l.Log.Warn("lease lost: work stopped; standing by", "lease", l.Name)
	}
	return nil
}

// hold runs work while renewing; lost is true when a renewal failed or
// found the lease taken (work was cancelled for that reason).
func (l *Lease) hold(ctx context.Context, work func(context.Context) error) (lost bool, err error) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lostFlag bool
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		t := time.NewTicker(l.Renew)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-wctx.Done():
				return
			case <-t.C:
			}
			rctx, rcancel := context.WithTimeout(ctx, callTimeout)
			held, err := l.Claim(rctx)
			rcancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil || !held {
				l.Log.Warn("renewing the lease failed", "lease", l.Name, "held_by_another", err == nil, "err", err)
				lostFlag = true
				cancel()
				return
			}
		}
	}()
	err = work(wctx)
	close(done)
	<-renewed
	return lostFlag, err
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
