// Package worker runs jobs from the queue: it leases due jobs of the kinds
// it has handlers for, runs up to Concurrency at a time, and reports each
// outcome back (the next run time, or a failure that is retried later).
//
// Kill switches (plan §8.8) turn a feature off without a restart: a
// switched-off kind is simply not leased.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

// Handler runs one job. It returns when the job should run next (zero: the
// job is finished for good) or an error (the job is retried with backoff).
type Handler func(ctx context.Context, j queue.Job) (next time.Time, err error)

// Worker leases and runs jobs.
type Worker struct {
	Pool        *pgxpool.Pool
	Owner       string // this process, for the lease_owner column
	Handlers    map[string]Handler
	Concurrency int
	Lease       time.Duration // how long a job may run before others may take it
	Idle        time.Duration // wait when nothing is due
	Log         *slog.Logger

	switches Switches
	swAt     time.Time
}

// SwitchesKey is the settings key of the kill switches.
const SwitchesKey = "kill_switches"

// Switches maps a job kind (or "all") to off=true.
type Switches map[string]bool

// LoadSwitches reads the kill switches.
func LoadSwitches(ctx context.Context, q db.Querier) (Switches, error) {
	s := Switches{}
	_, err := db.GetSetting(ctx, q, SwitchesKey, &s)
	return s, err
}

// SetSwitch turns a kind (or "all") off or back on.
func SetSwitch(ctx context.Context, q db.Querier, kind string, off bool) error {
	s, err := LoadSwitches(ctx, q)
	if err != nil {
		return err
	}
	if off {
		s[kind] = true
	} else {
		delete(s, kind)
	}
	return db.SetSetting(ctx, q, SwitchesKey, s)
}

func (w *Worker) kinds(ctx context.Context) []string {
	if time.Since(w.swAt) > 30*time.Second {
		if s, err := LoadSwitches(ctx, w.Pool); err == nil {
			w.switches, w.swAt = s, time.Now()
		} else if w.Log != nil {
			w.Log.Warn("reading kill switches failed", "err", err)
		}
	}
	if w.switches["all"] {
		return nil
	}
	var ks []string
	for k := range w.Handlers {
		if !w.switches[k] {
			ks = append(ks, k)
		}
	}
	return ks
}

// Run works until ctx ends, then waits for running jobs to finish.
func (w *Worker) Run(ctx context.Context) error {
	if w.Concurrency <= 0 {
		w.Concurrency = 4
	}
	if w.Lease <= 0 {
		w.Lease = 15 * time.Minute
	}
	if w.Idle <= 0 {
		w.Idle = 5 * time.Second
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	slots := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for ctx.Err() == nil {
		free := w.Concurrency - len(slots)
		kinds := w.kinds(ctx)
		var jobs []queue.Job
		if free > 0 && len(kinds) > 0 {
			var err error
			jobs, err = queue.Lease(ctx, w.Pool, w.Owner, kinds, free, w.Lease)
			if err != nil && ctx.Err() == nil {
				w.Log.Warn("leasing jobs failed", "err", err)
			}
		}
		if len(jobs) == 0 {
			select {
			case <-ctx.Done():
			case <-time.After(w.Idle):
			}
			continue
		}
		for _, j := range jobs {
			slots <- struct{}{}
			wg.Add(1)
			go func(j queue.Job) {
				defer func() { <-slots; wg.Done() }()
				w.run(ctx, j)
			}(j)
		}
		// Wait for a free slot before leasing more.
		select {
		case <-ctx.Done():
		case slots <- struct{}{}:
			<-slots
		}
	}
	return nil
}

func (w *Worker) run(ctx context.Context, j queue.Job) {
	h := w.Handlers[j.Kind]
	start := time.Now()
	next, err := safe(ctx, h, j)
	// Report with a fresh context: a shutdown must not lose the outcome.
	rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			// Shutting down: hand the job back so it runs again at once.
			_ = queue.Release(rctx, w.Pool, j)
			return
		}
		w.Log.Warn("job failed", "job", j.String(), "attempt", j.Attempts, "took", time.Since(start).Round(time.Millisecond), "err", err)
		if ferr := queue.Fail(rctx, w.Pool, j, err); ferr != nil {
			w.Log.Error("recording a failure failed", "job", j.String(), "err", ferr)
		}
		return
	}
	if derr := queue.Done(rctx, w.Pool, j, next); derr != nil {
		w.Log.Error("recording a finished job failed", "job", j.String(), "err", derr)
	}
}

// safe turns a panic in a handler into an error, so one bad page cannot
// stop the worker.
func safe(ctx context.Context, h Handler, j queue.Job) (next time.Time, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("handler panic")
			slog.Default().Error("handler panic", "job", j.String(), "panic", r)
		}
	}()
	return h(ctx, j)
}
