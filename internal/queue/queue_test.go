package queue_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

func TestLeaseOrderAndOneJobPerChannel(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(queue.Put(ctx, pool, "video", "fakeV1", "UCfakeA", queue.P2, past))
	must(queue.Put(ctx, pool, "comments", "fakeV1", "UCfakeA", queue.P1, past.Add(time.Minute)))
	must(queue.Put(ctx, pool, "rss", "UCfakeB", "UCfakeB", queue.P2, past.Add(-time.Minute)))
	must(queue.Put(ctx, pool, "enrich", "UCfakeAuthor", "", queue.P4, past))
	must(queue.Put(ctx, pool, "video", "fakeLater", "UCfakeC", queue.P0, time.Now().Add(time.Hour)))

	jobs, err := queue.Lease(ctx, pool, "w1", nil, 10, time.Minute)
	must(err)
	got := []string{}
	for _, j := range jobs {
		got = append(got, j.String())
	}
	// P1 comments of channel A first; A's video job waits for it; then B,
	// then the P4 job without a channel. The future job is not due.
	want := []string{"comments/fakeV1", "rss/UCfakeB", "enrich/UCfakeAuthor"}
	if len(got) != len(want) {
		t.Fatalf("leased %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leased %v; want %v", got, want)
		}
	}
	if jobs[0].Attempts != 1 {
		t.Errorf("attempts %d", jobs[0].Attempts)
	}
	// While channel A's job runs, its other job is not handed out.
	more, err := queue.Lease(ctx, pool, "w2", nil, 10, time.Minute)
	must(err)
	if len(more) != 0 {
		t.Fatalf("leased %v while channel A is busy", more)
	}
	must(queue.Done(ctx, pool, jobs[0], time.Now().Add(3*time.Hour)))
	more, err = queue.Lease(ctx, pool, "w2", []string{"video"}, 10, time.Minute)
	must(err)
	if len(more) != 1 || more[0].String() != "video/fakeV1" {
		t.Fatalf("after A's comments finished: %v", more)
	}
}

func TestLeaseExpiresAndConcurrentWorkersNeverShare(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		id := "fakeV" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		if err := queue.Put(ctx, pool, "video", id, "UCfake"+id, queue.P2, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				jobs, err := queue.Lease(ctx, pool, "w", nil, 3, 200*time.Millisecond)
				if err != nil {
					t.Error(err)
					return
				}
				if len(jobs) == 0 {
					return
				}
				mu.Lock()
				for _, j := range jobs {
					seen[j.EntityID]++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Fatalf("leased %d distinct jobs; want 40", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s leased %d times by concurrent workers", id, n)
		}
	}
	// A crashed worker's lease runs out and the job comes back.
	time.Sleep(300 * time.Millisecond)
	jobs, err := queue.Lease(ctx, pool, "w", nil, 100, time.Minute)
	if err != nil || len(jobs) != 40 {
		t.Fatalf("after the leases expired: %d jobs, err %v", len(jobs), err)
	}
	if jobs[0].Attempts != 2 {
		t.Errorf("attempts %d; want 2", jobs[0].Attempts)
	}
}

func TestCheckNowDuringARunAndFailures(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	if err := queue.Put(ctx, pool, "rss", "UCfakeX", "UCfakeX", queue.P2, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	jobs, _ := queue.Lease(ctx, pool, "w", nil, 1, time.Minute)
	if len(jobs) != 1 {
		t.Fatal("no job")
	}
	// A push notification says "check now" while the job runs.
	if err := queue.Now(ctx, pool, "rss", "UCfakeX", "UCfakeX", queue.P0); err != nil {
		t.Fatal(err)
	}
	if err := queue.Done(ctx, pool, jobs[0], time.Now().Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	again, _ := queue.Lease(ctx, pool, "w", nil, 1, time.Minute)
	if len(again) != 1 || again[0].Priority != queue.P0 {
		t.Fatalf("the check-now during the run was lost: %+v", again)
	}
	// A failure retries after the backoff, not at once.
	if dead, err := queue.Fail(ctx, pool, again[0], errors.New("HTTP 503"), true); err != nil || dead {
		t.Fatal(dead, err)
	}
	if next, _ := queue.Lease(ctx, pool, "w", nil, 1, time.Minute); len(next) != 0 {
		t.Fatalf("a failed job came back before its backoff: %+v", next)
	}
	st, err := queue.Stats(ctx, pool)
	if err != nil || len(st) != 1 || st[0].Failing != 1 || st[0].Due != 0 {
		t.Errorf("stats %+v, err %v", st, err)
	}
	// Done with a zero time removes the row.
	if err := queue.Put(ctx, pool, "resolve", "@fakehandle", "", queue.P0, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	r, _ := queue.Lease(ctx, pool, "w", []string{"resolve"}, 1, time.Minute)
	if len(r) != 1 {
		t.Fatal("resolve job not leased")
	}
	if err := queue.Done(ctx, pool, r[0], time.Time{}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM schedule WHERE kind = 'resolve'`).Scan(&n)
	if n != 0 {
		t.Error("a finished one-off job must be removed")
	}
}

func TestPutNeverDelays(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	soon := time.Now().Add(time.Minute).Truncate(time.Microsecond)
	_ = queue.Put(ctx, pool, "video", "fakeV9", "", queue.P0, soon)
	_ = queue.Put(ctx, pool, "video", "fakeV9", "", queue.P3, soon.Add(time.Hour))
	var prio int
	var due time.Time
	if err := pool.QueryRow(ctx, `SELECT priority, due_at FROM schedule WHERE entity_id = 'fakeV9'`).Scan(&prio, &due); err != nil {
		t.Fatal(err)
	}
	if prio != queue.P0 || !due.Equal(soon) {
		t.Errorf("priority %d, due %v; a later, lower put must not delay an earlier one", prio, due)
	}
}

func TestBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{0: 2 * time.Minute, 1: 2 * time.Minute, 2: 4 * time.Minute, 4: 16 * time.Minute, 20: 6 * time.Hour} {
		if got := queue.Backoff(attempts); got != want {
			t.Errorf("Backoff(%d) = %v; want %v", attempts, got, want)
		}
	}
}

// Bounded retries (REQ-010 fix 3): item failures kill a job at its kind's
// limit; systemic ones (blocks, no free IP) only delay it. A dead job keeps
// its row and reason, is never leased, and comes back with Requeue.
func TestDeadJobsAndRequeue(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	_ = queue.Put(ctx, pool, "media", "thumb:fakeVid0001", "", queue.P2, past)
	_ = queue.Put(ctx, pool, "video", "fakeVid0002", "", queue.P2, past)
	lease := func(kind string) queue.Job {
		t.Helper()
		_, _ = pool.Exec(ctx, `UPDATE schedule SET due_at = now() - interval '1 second' WHERE kind = $1`, kind)
		js, err := queue.Lease(ctx, pool, "w", []string{kind}, 1, time.Minute)
		if err != nil || len(js) != 1 {
			t.Fatalf("lease %s: %v %v", kind, js, err)
		}
		return js[0]
	}

	// Systemic failures never kill: far more than the limit, still alive.
	for i := 0; i < 3*queue.MaxFailures("video"); i++ {
		if dead, err := queue.Fail(ctx, pool, lease("video"), errors.New("blocked"), false); err != nil || dead {
			t.Fatalf("systemic failure %d: dead=%v err=%v", i, dead, err)
		}
	}

	// Item failures: dead exactly at the limit (5 for media).
	limit := queue.MaxFailures("media")
	if limit != 5 || queue.MaxFailures("video") != queue.DefaultMaxFailures {
		t.Fatalf("limits: media %d, video %d", limit, queue.MaxFailures("video"))
	}
	for i := 1; i <= limit; i++ {
		dead, err := queue.Fail(ctx, pool, lease("media"), errors.New("image is not a JPEG"), true)
		if err != nil || dead != (i == limit) {
			t.Fatalf("item failure %d of %d: dead=%v err=%v", i, limit, dead, err)
		}
	}
	_, _ = pool.Exec(ctx, `UPDATE schedule SET due_at = now() - interval '1 second'`)
	if js, _ := queue.Lease(ctx, pool, "w", []string{"media"}, 5, time.Minute); len(js) != 0 {
		t.Fatalf("a dead job was leased: %+v", js)
	}
	st, _ := queue.Stats(ctx, pool)
	for _, d := range st {
		if d.Kind == "media" && (d.Dead != 1 || d.Due != 0 || d.Failing != 0) {
			t.Errorf("media stats %+v; want 1 dead, not due, not counted as failing", d)
		}
	}
	dj, err := queue.DeadJobs(ctx, pool)
	if err != nil || len(dj) != 1 || dj[0].Kind != "media" || dj[0].Reason != "image is not a JPEG" || dj[0].Count != 1 {
		t.Fatalf("dead jobs %+v, %v", dj, err)
	}
	// A check-now doesn't revive it; only Requeue does.
	_ = queue.Now(ctx, pool, "media", "thumb:fakeVid0001", "", queue.P0)
	if js, _ := queue.Lease(ctx, pool, "w", []string{"media"}, 5, time.Minute); len(js) != 0 {
		t.Fatal("a check-now revived a dead job")
	}
	if n, err := queue.Requeue(ctx, pool, "video", ""); err != nil || n != 0 {
		t.Errorf("requeue of another kind brought back %d, %v", n, err)
	}
	if n, err := queue.Requeue(ctx, pool, "media", ""); err != nil || n != 1 {
		t.Fatalf("requeue brought back %d, %v", n, err)
	}
	js, _ := queue.Lease(ctx, pool, "w", []string{"media"}, 5, time.Minute)
	if len(js) != 1 || js[0].Attempts != 1 {
		t.Fatalf("after requeue: %+v; want leasable again, counters reset", js)
	}
	// One more item failure after a requeue doesn't kill at once.
	if dead, _ := queue.Fail(ctx, pool, js[0], errors.New("image is not a JPEG"), true); dead {
		t.Error("the failure count wasn't reset by the requeue")
	}
	// A success resets the count too.
	j := lease("video")
	if dead, _ := queue.Fail(ctx, pool, j, errors.New("parse error"), true); dead {
		t.Fatal("dead after one failure")
	}
	if err := queue.Done(ctx, pool, lease("video"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var failures int
	_ = pool.QueryRow(ctx, `SELECT failures FROM schedule WHERE kind = 'video'`).Scan(&failures)
	if failures != 0 {
		t.Errorf("failures after a success = %d", failures)
	}
}
