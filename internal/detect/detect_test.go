package detect_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/detect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/feeds"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
)

const (
	core    = "UCfakeDETECTcore00000001"
	mention = "UCfakeDETECTmention00001"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// Rig returns a feed reader against a fake YouTube with two listed
// channels, a core and a mention one.
func Rig(t *testing.T) (*fakeyt.Server, *pgxpool.Pool, *detect.RSS) {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	px := fakeyt.NewForwardProxy("a")
	t.Cleanup(px.Close)
	ps, _ := proxypool.ParseList([]string{px.ProxyLine()})
	pp := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true}, 1)
	pool := dbtest.New(t)
	ctx := context.Background()
	_, _ = kwstore.ImportSeed(ctx, pool)
	live := &kwstore.Live{}
	_, _ = live.Refresh(ctx, pool)
	_ = registry.List(ctx, pool, nil, core, registry.Input{Input: core, Tier: policy.A})
	_ = registry.List(ctx, pool, nil, mention, registry.Input{Input: mention, Kind: registry.Mention})
	_, _ = pool.Exec(ctx, `DELETE FROM schedule WHERE kind <> 'rss'`)
	rss := &detect.RSS{Pool: pool, Feeds: feeds.NewFetcher(innertube.New(innertube.Options{Pool: pp, BaseURL: yt.URL})),
		Write: &writer.Writer{Pool: pool, KW: live}, Log: quiet}
	return yt, pool, rss
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func run(t *testing.T, pool *pgxpool.Pool, h map[string]worker.Handler) {
	t.Helper()
	w := &worker.Worker{Pool: pool, Owner: "t", Handlers: h, Concurrency: 2, Idle: 10 * time.Millisecond, Log: quiet}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)
}

// runUntil runs the worker until done reports true, or for at most 10
// seconds. A fixed window is too short when CI's shared database is busy
// creating other packages' databases.
func runUntil(t *testing.T, pool *pgxpool.Pool, h map[string]worker.Handler, done func() bool) {
	t.Helper()
	w := &worker.Worker{Pool: pool, Owner: "t", Handlers: h, Concurrency: 2, Idle: 10 * time.Millisecond, Log: quiet}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = w.Run(ctx)
	}()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !done() {
		select {
		case <-ctx.Done():
			<-stopped
			return
		case <-tick.C:
		}
	}
	cancel()
	<-stopped
}

func TestFeedsQueueNewUploads(t *testing.T) {
	yt, pool, rss := Rig(t)
	now := time.Now().Add(-10 * time.Minute)
	yt.SetFeed(core, []fakeyt.FeedEntry{
		{VideoID: "fakeDv1", ChannelID: core, Title: "any title", Published: now, Views: 10, Likes: 1},
		{VideoID: "fakeDv2", ChannelID: core, Title: "older", Published: now.Add(-time.Hour), Views: 99, Likes: 3},
	})
	yt.SetFeed(mention, []fakeyt.FeedEntry{
		{VideoID: "fakeDm1", ChannelID: mention, Title: "أخبار بغداد", Published: now},
		{VideoID: "fakeDm2", ChannelID: mention, Title: "cooking show", Published: now},
	})
	// fakeDv2 is already known: its feed numbers become a snapshot.
	_, _ = pool.Exec(context.Background(), `INSERT INTO videos (id, channel_id, kind) VALUES ('fakeDv2', $1, 'video')`, core)
	run(t, pool, rss.Handlers())

	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = 'video' AND priority = 0`); n != 2 {
		t.Errorf("%d videos queued at P0; want the core channel's new upload and the relevant mention one", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = 'video' AND entity_id = 'fakeDm2'`); n != 0 {
		t.Error("a mention channel's unrelated upload is noted, not collected")
	}
	if n := count(t, pool, `SELECT count(*) FROM videos WHERE id = 'fakeDm2' AND NOT relevant AND source = 'rss'`); n != 1 {
		t.Error("the unrelated upload is still noted, with its source")
	}
	if n := count(t, pool, `SELECT count(*) FROM videos WHERE id = 'fakeDv1' AND source = 'rss' AND published_precision_s = 0`); n != 1 {
		t.Error("a feed upload has an exact publish time")
	}
	if n := count(t, pool, `SELECT count(*) FROM video_snapshots WHERE video_id = 'fakeDv2' AND views = 99 AND likes_min = 3`); n != 1 {
		t.Error("the feed's exact numbers for a known video become a snapshot")
	}
	var due time.Time
	_ = pool.QueryRow(context.Background(), `SELECT due_at FROM schedule WHERE kind = 'rss' AND entity_id = $1`, core).Scan(&due)
	if d := time.Until(due); d < 25*time.Minute || d > 31*time.Minute {
		t.Errorf("tier A's next feed read in %v; want 30 minutes", d)
	}
	// A second read finds nothing new (304 or the same entries).
	_, _ = pool.Exec(context.Background(), `UPDATE schedule SET due_at = now() WHERE kind = 'rss'`)
	_, _ = pool.Exec(context.Background(), `DELETE FROM schedule WHERE kind = 'video'`)
	run(t, pool, rss.Handlers())
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = 'video'`); n != 0 {
		t.Errorf("a re-read queued %d videos again", n)
	}
}

func TestBreakerOpensWhenFeedsFailWidely(t *testing.T) {
	var b detect.Breaker
	now := time.Now()
	for i := 0; i < 15; i++ {
		b.Record(now, true)
	}
	opened := false
	for i := 0; i < 5; i++ {
		opened = b.Record(now, false) || opened
	}
	if !opened || !b.Open(now) {
		t.Fatal("5 failures in 20 reads (25%) must open the breaker")
	}
	if b.Open(now.Add(31 * time.Minute)) {
		t.Error("the breaker closes after 30 minutes")
	}
	var c detect.Breaker
	for i := 0; i < 100; i++ {
		c.Record(now, i%10 != 0) // 10% failing
	}
	if c.Open(now) {
		t.Error("10% failures must not open it")
	}
}

// While the feed service answers "not found", the channel's tabs stand in:
// on a 404, and for every channel without a feed request while the breaker
// is open. The next read is at the normal interval.
func TestFeedOutageReadsTabs(t *testing.T) {
	yt, pool, rss := Rig(t) // no feeds set: the fake answers 404
	// The worker runs jobs on several goroutines at once, so the fake
	// records its calls under a lock.
	var mu sync.Mutex
	var calls []string
	rss.Recent = func(_ context.Context, channelID string) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, channelID)
		return 1, nil
	}
	recent := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
	runUntil(t, pool, rss.Handlers(), func() bool { return len(recent()) >= 2 })
	if got := recent(); len(got) != 2 {
		t.Fatalf("tabs read for %v; want both channels after their 404s", got)
	}
	var due time.Time
	_ = pool.QueryRow(context.Background(), `SELECT due_at FROM schedule WHERE kind = 'rss' AND entity_id = $1`, core).Scan(&due)
	if d := time.Until(due); d < 25*time.Minute || d > 31*time.Minute {
		t.Errorf("next read in %v; want tier A's 30 minutes, as with a working feed", d)
	}

	// Breaker open: no feed request at all, the tabs are read directly.
	b := &detect.Breaker{}
	now := time.Now()
	for i := 0; i < 20; i++ {
		b.Record(now, false)
	}
	rss.Breaker = b
	feedsBefore := 0
	for _, r := range yt.Requests() {
		if r.Endpoint == "feed" {
			feedsBefore++
		}
	}
	mu.Lock()
	calls = nil
	mu.Unlock()
	if _, err := pool.Exec(context.Background(), `UPDATE schedule SET due_at = now() WHERE kind = 'rss'`); err != nil {
		t.Fatal(err)
	}
	runUntil(t, pool, rss.Handlers(), func() bool { return len(recent()) >= 2 })
	feedsAfter := 0
	for _, r := range yt.Requests() {
		if r.Endpoint == "feed" {
			feedsAfter++
		}
	}
	if got := recent(); len(got) != 2 || feedsAfter != feedsBefore {
		t.Errorf("breaker open: tabs read %d times, %d feed requests; want 2 and none", len(got), feedsAfter-feedsBefore)
	}
}
