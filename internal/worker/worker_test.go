package worker_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/usage"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
)

func TestWorkerRunsRetriesAndHonoursKillSwitches(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	for _, id := range []string{"fakeA", "fakeB", "fakeC"} {
		_ = queue.Put(ctx, pool, "video", id, "", queue.P2, past)
	}
	_ = queue.Put(ctx, pool, "comments", "fakeA", "", queue.P1, past)
	_ = queue.Put(ctx, pool, "boom", "fakeX", "", queue.P2, past)
	if err := worker.SetSwitch(ctx, pool, "comments", true); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	ran := map[string]int{}
	var running, maxRunning atomic.Int32
	w := &worker.Worker{
		Pool: pool, Owner: "test", Concurrency: 2, Idle: 20 * time.Millisecond, Lease: time.Minute,
		Handlers: map[string]worker.Handler{
			"video": func(ctx context.Context, j queue.Job) (time.Time, error) {
				n := running.Add(1)
				defer running.Add(-1)
				for {
					m := maxRunning.Load()
					if n <= m || maxRunning.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
				mu.Lock()
				ran[j.EntityID]++
				mu.Unlock()
				if j.EntityID == "fakeC" && j.Attempts == 1 {
					return time.Time{}, errors.New("HTTP 503")
				}
				return time.Now().Add(time.Hour), nil
			},
			"comments": func(context.Context, queue.Job) (time.Time, error) {
				t.Error("a switched-off kind must not run")
				return time.Time{}, nil
			},
			"boom": func(context.Context, queue.Job) (time.Time, error) { panic("bad page") },
		},
	}
	wctx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	if err := w.Run(wctx); err != nil {
		t.Fatal(err)
	}
	if ran["fakeA"] != 1 || ran["fakeB"] != 1 || ran["fakeC"] != 1 {
		t.Errorf("ran %v; each video once (fakeC's retry waits for its backoff)", ran)
	}
	if maxRunning.Load() > 2 {
		t.Errorf("%d jobs ran at once; the limit is 2", maxRunning.Load())
	}
	st, _ := queue.Stats(ctx, pool)
	failing := map[string]int64{}
	for _, d := range st {
		failing[d.Kind] += d.Failing
	}
	if failing["video"] != 1 || failing["boom"] != 1 {
		t.Errorf("failing %v; want fakeC's 503 and the panicking job recorded", failing)
	}
	// "all" stops everything.
	_ = worker.SetSwitch(ctx, pool, "comments", false)
	_ = worker.SetSwitch(ctx, pool, "all", true)
	sw, _ := worker.LoadSwitches(ctx, pool)
	if !sw["all"] || sw["comments"] {
		t.Errorf("switches %v", sw)
	}
}

// A shutdown lets running jobs finish within the drain time (standard B4).
func TestWorkerDrainLetsRunningJobsFinish(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	_ = queue.Put(ctx, pool, "video", "fakeSlow", "", queue.P2, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	w := &worker.Worker{
		Pool: pool, Owner: "test-drain", Concurrency: 1, Idle: 20 * time.Millisecond, Lease: time.Minute, Drain: 5 * time.Second,
		Handlers: map[string]worker.Handler{
			"video": func(ctx context.Context, j queue.Job) (time.Time, error) {
				close(started)
				select {
				case <-time.After(300 * time.Millisecond): // finishes inside the drain time
					return time.Now().Add(time.Hour), nil
				case <-ctx.Done():
					return time.Time{}, ctx.Err()
				}
			},
		},
	}
	wctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { errc <- w.Run(wctx) }()
	<-started
	cancel() // the shutdown signal, while the job runs
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	var due time.Time
	var leased, ok bool
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT due_at, lease_until IS NOT NULL, last_ok IS NOT NULL, attempts FROM schedule
		WHERE kind = 'video' AND entity_id = 'fakeSlow'`).Scan(&due, &leased, &ok, &attempts); err != nil {
		t.Fatal(err)
	}
	if !ok || leased || attempts != 0 || time.Until(due) < 30*time.Minute {
		t.Errorf("finished=%v leased=%v attempts=%d due in %v; the job should have finished and been rescheduled",
			ok, leased, attempts, time.Until(due).Round(time.Minute))
	}
}

// A job still running when the drain time is over is interrupted and handed
// back unrun: due at once, the attempt not counted, no error recorded.
func TestWorkerDrainInterruptsAndHandsBack(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	_ = queue.Put(ctx, pool, "video", "fakeStuck", "", queue.P2, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	w := &worker.Worker{
		Pool: pool, Owner: "test-interrupt", Concurrency: 1, Idle: 20 * time.Millisecond, Lease: time.Minute,
		Drain: 200 * time.Millisecond,
		Handlers: map[string]worker.Handler{
			"video": func(ctx context.Context, j queue.Job) (time.Time, error) {
				close(started)
				<-ctx.Done()
				// Handlers often wrap the cancellation in their own error.
				return time.Time{}, errors.New("browse: request aborted")
			},
		},
	}
	wctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { errc <- w.Run(wctx) }()
	<-started
	stopAt := time.Now()
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if took := time.Since(stopAt); took < 200*time.Millisecond || took > 3*time.Second {
		t.Errorf("Run returned %v after the signal; want just after the 200ms drain", took)
	}
	var due time.Time
	var leased bool
	var attempts int
	var lastErr *string
	if err := pool.QueryRow(ctx, `SELECT due_at, lease_until IS NOT NULL, attempts, last_error FROM schedule
		WHERE kind = 'video' AND entity_id = 'fakeStuck'`).Scan(&due, &leased, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if leased || attempts != 0 || lastErr != nil || time.Until(due) > time.Second {
		t.Errorf("leased=%v attempts=%d last_error=%v due in %v; want handed back, due now, no failure", leased, attempts,
			lastErr, time.Until(due))
	}
}

// ReleaseOwner hands back only the stopping worker's own leases.
func TestReleaseOwner(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	_ = queue.Put(ctx, pool, "video", "fakeMine", "", queue.P2, past)
	_ = queue.Put(ctx, pool, "comments", "fakeTheirs", "", queue.P2, past)
	if _, err := queue.Lease(ctx, pool, "w-mine", []string{"video"}, 5, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Lease(ctx, pool, "w-theirs", []string{"comments"}, 5, time.Hour); err != nil {
		t.Fatal(err)
	}
	n, err := queue.ReleaseOwner(ctx, pool, "w-mine")
	if err != nil || n != 1 {
		t.Fatalf("released %d, %v; want 1", n, err)
	}
	var mine, theirs bool
	_ = pool.QueryRow(ctx, `SELECT lease_until IS NOT NULL FROM schedule WHERE entity_id = 'fakeMine'`).Scan(&mine)
	_ = pool.QueryRow(ctx, `SELECT lease_until IS NOT NULL FROM schedule WHERE entity_id = 'fakeTheirs'`).Scan(&theirs)
	if mine || !theirs {
		t.Errorf("mine leased=%v, theirs leased=%v; want only mine handed back", mine, theirs)
	}
}

// What each job used is recorded, failed jobs included (REQ-010 fix 5).
func TestWorkerRecordsUsage(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	ch := "UCfakeUSAGEAAAAAAAAAAAAA"
	_ = queue.Put(ctx, pool, queue.KindVideo, "fakevid0001", ch, queue.P2, past)
	_ = queue.Put(ctx, pool, queue.KindVideo, "fakevid0002", ch, queue.P2, past)
	w := &worker.Worker{
		Pool: pool, Owner: "test", Concurrency: 2, Idle: 20 * time.Millisecond, Lease: time.Minute,
		Handlers: map[string]worker.Handler{
			queue.KindVideo: func(ctx context.Context, j queue.Job) (time.Time, error) {
				usage.From(ctx).Request(1000)
				usage.From(ctx).Request(500)
				if j.EntityID == "fakevid0002" {
					usage.From(ctx).Block()
					return time.Time{}, errors.New("HTTP 500")
				}
				return time.Now().Add(time.Hour), nil
			},
		},
	}
	wctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := w.Run(wctx); err != nil {
		t.Fatal(err)
	}
	var jobs, fails, reqs, bytes, blocks int64
	if err := pool.QueryRow(ctx, `SELECT sum(jobs), sum(failures), sum(requests), sum(bytes), sum(blocks)
		FROM usage_hourly WHERE kind = 'video'`).Scan(&jobs, &fails, &reqs, &bytes, &blocks); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || fails != 1 || reqs != 4 || bytes != 3000 || blocks != 1 {
		t.Errorf("hourly = %d jobs, %d failed, %d requests, %d bytes, %d blocks", jobs, fails, reqs, bytes, blocks)
	}
	if err := pool.QueryRow(ctx, `SELECT sum(requests) FROM usage_channel_daily WHERE channel_id = $1`, ch).Scan(&reqs); err != nil || reqs != 4 {
		t.Errorf("channel requests = %d (%v); want 4", reqs, err)
	}
}
