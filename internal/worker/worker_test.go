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
