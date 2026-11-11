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

// Within a priority, tier A's jobs go first, then tier B's, then the rest
// (C, D, M and jobs of no channel) oldest due first; a higher priority
// still comes before any tier (the owner, D-052). A channel's tier, set or
// changed later, carries its queued rows along.
func TestLeaseTakesTiersAAndBFirst(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO channels (id, kind, tier, status) VALUES
		('UCfakeTIERA', 'core', 'A', 'listed'), ('UCfakeTIERB', 'core', 'B', 'listed'),
		('UCfakeTIERC', 'core', 'C', 'listed'), ('UCfakeTIERC2', 'core', 'C', 'listed')`)
	must(err)
	now := time.Now()
	must(queue.Put(ctx, pool, "comments", "fakeTA", "UCfakeTIERA", queue.P1, now.Add(-1*time.Hour)))
	must(queue.Put(ctx, pool, "comments", "fakeTB", "UCfakeTIERB", queue.P1, now.Add(-2*time.Hour)))
	must(queue.Put(ctx, pool, "comments", "fakeTC", "UCfakeTIERC", queue.P1, now.Add(-3*time.Hour)))
	must(queue.Put(ctx, pool, "search", "fake tier term", "", queue.P1, now.Add(-4*time.Hour)))
	must(queue.Put(ctx, pool, "video", "fakeTC0", "UCfakeTIERC2", queue.P0, now.Add(-time.Minute)))

	jobs, err := queue.Lease(ctx, pool, "w1", nil, 10, time.Minute)
	must(err)
	got := []string{}
	for _, j := range jobs {
		got = append(got, j.String())
	}
	want := []string{"video/fakeTC0", "comments/fakeTA", "comments/fakeTB", "search/fake tier term", "comments/fakeTC"}
	if len(got) != len(want) {
		t.Fatalf("leased %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leased %v; want %v", got, want)
		}
	}

	rank := func(kind, id string) int {
		t.Helper()
		var r int
		must(pool.QueryRow(ctx, `SELECT tier_rank FROM schedule WHERE kind = $1 AND entity_id = $2`, kind, id).Scan(&r))
		return r
	}
	// A channel moved to tier A takes its rows along.
	_, err = pool.Exec(ctx, `UPDATE channels SET tier = 'A' WHERE id = 'UCfakeTIERC'`)
	must(err)
	if r := rank("comments", "fakeTC"); r != 0 {
		t.Errorf("after the move to tier A: rank %d", r)
	}
	// A job queued before its channel is listed takes the tier once it is.
	must(queue.Put(ctx, pool, "rss", "UCfakeTIERLATE", "UCfakeTIERLATE", queue.P0, now))
	if r := rank("rss", "UCfakeTIERLATE"); r != 2 {
		t.Errorf("no channel yet: rank %d", r)
	}
	_, err = pool.Exec(ctx, `INSERT INTO channels (id, kind, tier, status) VALUES ('UCfakeTIERLATE', 'core', 'B', 'listed')`)
	must(err)
	if r := rank("rss", "UCfakeTIERLATE"); r != 1 {
		t.Errorf("listed in tier B: rank %d", r)
	}
	// A row whose channel is filled in later takes that channel's rank.
	must(queue.Put(ctx, pool, "video", "fakeTLATER", "", queue.P0, now))
	must(queue.Put(ctx, pool, "video", "fakeTLATER", "UCfakeTIERA", queue.P0, now))
	if r := rank("video", "fakeTLATER"); r != 0 {
		t.Errorf("channel filled in: rank %d", r)
	}
}

// After a run, a job's priority follows what it did (D-052): a video read
// once is a refresh. A check-now that raised it during the run is kept.
func TestReprioritize(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Minute)
	must(queue.Put(ctx, pool, "video", "fakeRP1", "", queue.P0, past))
	must(queue.Put(ctx, pool, "comments", "fakeRP2", "", queue.P1, past))
	jobs, err := queue.Lease(ctx, pool, "w1", nil, 10, time.Minute)
	must(err)
	if len(jobs) != 2 {
		t.Fatalf("leased %v", jobs)
	}
	for _, j := range jobs {
		if j.Kind == "comments" {
			must(queue.Now(ctx, pool, "comments", "fakeRP2", "", queue.P0)) // a check-now during the run
		}
		must(queue.Reprioritize(ctx, pool, j, queue.P2))
	}
	prio := func(kind, id string) int {
		t.Helper()
		var p int
		must(pool.QueryRow(ctx, `SELECT priority FROM schedule WHERE kind = $1 AND entity_id = $2`, kind, id).Scan(&p))
		return p
	}
	if p := prio("video", "fakeRP1"); p != queue.P2 {
		t.Errorf("the video read once is at P%d; want P2", p)
	}
	if p := prio("comments", "fakeRP2"); p != queue.P0 {
		t.Errorf("the check-now's P0 must be kept, not P%d", p)
	}
	if err := queue.Reprioritize(ctx, pool, jobs[0], 7); err == nil {
		t.Error("priority 7 is out of range")
	}
	for _, c := range []struct {
		age  time.Duration
		want int
	}{{0, queue.P1}, {47 * time.Hour, queue.P1}, {48 * time.Hour, queue.P2}, {30 * 24 * time.Hour, queue.P2}} {
		if got := queue.ScanPriority(c.age); got != c.want {
			t.Errorf("a comment scan at age %v: P%d, want P%d", c.age, got, c.want)
		}
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

// Overdue work by tier, kind and priority, and how much of each tier's the
// 10 busiest channels hold. Jobs not yet due, running or dead aren't
// overdue.
func TestOverdueWork(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO channels (id, tier, status) VALUES ('UCfakeOVERDUEA0000000001', 'A', 'listed'), ('UCfakeOVERDUEA0000000002', 'A', 'listed'),
		('UCfakeOVERDUEM0000000001', 'M', 'listed')`)
	// Channel A1: 3 comment scans 2 hours overdue; A2: 1 refresh an hour
	// overdue; M1: 1 comment scan an hour overdue; a search 3 hours overdue.
	exec(`INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at) VALUES
		('comments', 'fakeOVv1', 'UCfakeOVERDUEA0000000001', 1, now() - interval '2 hours'),
		('comments', 'fakeOVv2', 'UCfakeOVERDUEA0000000001', 1, now() - interval '2 hours'),
		('comments', 'fakeOVv3', 'UCfakeOVERDUEA0000000001', 1, now() - interval '2 hours'),
		('video', 'fakeOVv4', 'UCfakeOVERDUEA0000000002', 2, now() - interval '1 hour'),
		('comments', 'fakeOVv5', 'UCfakeOVERDUEM0000000001', 1, now() - interval '1 hour'),
		('search', 'fake overdue', NULL, 2, now() - interval '3 hours'),
		('comments', 'fakeOVv6', 'UCfakeOVERDUEA0000000001', 1, now() + interval '1 hour')`)
	exec(`INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at, dead_at) VALUES
		('comments', 'fakeOVdead', 'UCfakeOVERDUEA0000000001', 1, now() - interval '9 hours', now())`)
	over, conc, err := queue.OverdueWork(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]queue.Overdue{}
	for _, o := range over {
		got[o.Tier+"/"+o.Kind] = o
	}
	if o := got["A/comments"]; o.Jobs != 3 || o.Priority != 1 || o.DebtHours < 5.9 || o.DebtHours > 6.1 {
		t.Errorf("A comment scans %+v; want 3 jobs, about 6 debt hours", o)
	}
	if o := got["A/video"]; o.Jobs != 1 || o.Priority != 2 {
		t.Errorf("A refresh %+v", o)
	}
	if o := got["-/search"]; o.Jobs != 1 || o.Oldest < 2*time.Hour+59*time.Minute {
		t.Errorf("search %+v; want 1 job about 3 hours overdue", o)
	}
	if len(over) != 4 {
		t.Errorf("%d rows; the job not yet due and the dead one aren't overdue: %+v", len(over), over)
	}
	byTier := map[string]queue.Concentration{}
	for _, c := range conc {
		byTier[c.Tier] = c
	}
	if c := byTier["A"]; c.Channels != 2 || c.Jobs != 4 || c.TopJobs != 4 || c.TopLargest != 3 {
		t.Errorf("tier A concentration %+v; want 2 channels, 4 jobs, the busiest with 3", c)
	}
	if _, ok := byTier["-"]; ok {
		t.Error("jobs of no listed channel have no concentration row")
	}
}
