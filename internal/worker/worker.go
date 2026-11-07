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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/provenance"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/rawstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/usage"
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
	// Drain is how long running jobs may keep working after Run's context
	// ends (the shutdown signal) before they are interrupted and handed
	// back. Zero interrupts them at once. Keep it below the pod's
	// termination grace period.
	Drain    time.Duration
	Log      *slog.Logger
	OnResult func(kind string, err error, took time.Duration) // metrics; nil = none
	// OnDead is called when a job reaches its failure limit; nil = none.
	OnDead func(kind string)

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

// interruptWait is how long interrupted jobs get to return and report
// before Run hands back whatever is still leased and returns anyway.
const interruptWait = 5 * time.Second

// Run works until ctx ends (standard B4). Then it leases nothing more, lets
// running jobs finish for up to Drain, interrupts the rest (each is handed
// back unrun, so another worker takes it at once), and finally hands back
// anything this worker still holds.
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
	// Jobs run on their own context, which outlives the shutdown signal by
	// up to Drain.
	work, interrupt := context.WithCancel(context.WithoutCancel(ctx))
	defer interrupt()
	slots := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	defer w.stop(&wg, interrupt)
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
				w.run(work, j)
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

// stop is the end of Run: drain, interrupt, hand back.
func (w *Worker) stop(wg *sync.WaitGroup, interrupt context.CancelFunc) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	if w.Drain > 0 {
		select {
		case <-done:
		case <-time.After(w.Drain):
			w.Log.Info("drain time over: interrupting running jobs", "drain", w.Drain.String())
		}
	}
	interrupt()
	select {
	case <-done:
	case <-time.After(interruptWait):
		w.Log.Warn("jobs still running after the interrupt; handing back their leases")
	}
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if n, err := queue.ReleaseOwner(rctx, w.Pool, w.Owner); err != nil {
		w.Log.Warn("handing back leases failed; they expire on their own", "err", err)
	} else if n > 0 {
		w.Log.Info("leases handed back", "jobs", n)
	}
}

func (w *Worker) run(ctx context.Context, j queue.Job) {
	h := w.Handlers[j.Kind]
	start := time.Now()
	// Renew the lease while the job runs, so a long job (a busy channel's
	// backfill) is not taken over by another worker.
	hb, stopHB := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(w.Lease / 3)
		defer t.Stop()
		for {
			select {
			case <-hb.Done():
				return
			case <-t.C:
				if err := queue.Extend(hb, w.Pool, j, w.Lease); err != nil && hb.Err() == nil {
					w.Log.Warn("renewing a lease failed", "job", j.String(), "err", err)
				}
			}
		}
	}()
	jctx, meter := usage.With(ctx)
	jctx, _ = rawstore.WithJob(jctx) // a parse failure keeps the job's latest raw answers
	// Where each write's data came from, for the outbox's events (fix 12).
	jctx, _ = provenance.With(jctx, provenance.Job{ID: fmt.Sprintf("%s/%s/%d", j.Kind, j.EntityID, start.UnixMilli()), Kind: j.Kind})
	next, err := safe(jctx, h, j)
	stopHB()
	took := time.Since(start)
	if w.OnResult != nil {
		w.OnResult(j.Kind, err, took)
	}
	// Report with a fresh context: a shutdown must not lose the outcome.
	rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	interrupted := err != nil && ctx.Err() != nil
	// What the job used counts even when it failed or was interrupted: the
	// requests were sent (REQ-010 fix 5).
	if uerr := usage.Record(rctx, w.Pool, start, j, meter.Totals(), err != nil && !interrupted, took); uerr != nil {
		w.Log.Warn("recording usage failed", "job", j.String(), "err", uerr)
	}
	if err != nil {
		if interrupted {
			// Interrupted at shutdown, whatever the handler made of it:
			// hand the job back unrun, so it runs again at once and the
			// attempt doesn't count.
			_ = queue.Release(rctx, w.Pool, j)
			return
		}
		item := !Systemic(err)
		w.Log.Warn("job failed", "job", j.String(), "attempt", j.Attempts, "counts", item,
			"took", took.Round(time.Millisecond), "err", err)
		dead, ferr := queue.Fail(rctx, w.Pool, j, err, item)
		if ferr != nil {
			w.Log.Error("recording a failure failed", "job", j.String(), "err", ferr)
		}
		if dead {
			w.Log.Warn("job dead: too many failures; requeue it once fixed", "job", j.String(), "limit", queue.MaxFailures(j.Kind))
			if w.OnDead != nil {
				w.OnDead(j.Kind)
			}
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
