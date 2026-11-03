package collect_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
)

const chID = "UCfakeCOLLECT00000000001"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type rig struct {
	yt   *fakeyt.Server
	pool *pgxpool.Pool
	svc  *collect.Service
	sup  *privacy.Suppressor
}

func newRig(t *testing.T, chans []fakeyt.FakeChannel) *rig {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	yt.ServeChannels(chans, 2, 2)
	p1, p2 := fakeyt.NewForwardProxy("a"), fakeyt.NewForwardProxy("b")
	t.Cleanup(p1.Close)
	t.Cleanup(p2.Close)
	ps, err := proxypool.ParseList([]string{p1.ProxyLine(), p2.ProxyLine()})
	if err != nil {
		t.Fatal(err)
	}
	pp := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true,
		FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond, TransientBackoff: time.Millisecond}, 1)
	debug, err := phase0.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fetch := &phase0.Collector{Client: innertube.New(innertube.Options{Pool: pp, BaseURL: yt.URL, CDNBase: yt.URL}), Store: debug, Now: time.Now, Log: quiet}
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := kwstore.ImportSeed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	live := &kwstore.Live{}
	if _, err := live.Refresh(ctx, pool); err != nil {
		t.Fatal(err)
	}
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	svc := &collect.Service{Pool: pool, Fetch: fetch, Sup: sup, Log: quiet, WithPlayer: true,
		Write: &writer.Writer{Pool: pool, Sup: sup, KW: live}}
	return &rig{yt: yt, pool: pool, svc: svc, sup: sup}
}

func (r *rig) work(t *testing.T, d time.Duration) {
	t.Helper()
	w := &worker.Worker{Pool: r.pool, Owner: "test", Handlers: r.svc.Handlers(), Concurrency: 4, Idle: 10 * time.Millisecond,
		Lease: time.Minute, Log: quiet}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_ = w.Run(ctx)
}

func (r *rig) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func fakeChannel() fakeyt.FakeChannel {
	return fakeyt.FakeChannel{ID: chID, Handle: "@fakecollect", Title: "قناة تجريبية", Country: "Iraq", Subscribers: "25K subscribers",
		Videos: []fakeyt.FakeVideo{
			{ID: "fakeCV1", Title: "أخبار بغداد اليوم", Views: 1200, Likes: 40, Ago: "2 hours ago", LengthSec: 300, CommentsTxt: "3",
				PublishDate: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)},
			{ID: "fakeCV2", Title: "recipe of the day", Views: 50, Likes: 2, Ago: "3 days ago", LengthSec: 120, CommentsTxt: "0"},
			{ID: "fakeCV3", Title: "short from Basra", Views: 999, Ago: "5 days ago", LengthSec: 30, Short: true, CommentsTxt: "1"},
			{ID: "fakeCVold", Title: "two years old", Views: 5, Ago: "2 years ago", LengthSec: 60, CommentsTxt: "0"},
		}}
}

func TestAddByHandleBackfillsAndCollectsVideos(t *testing.T) {
	r := newRig(t, []fakeyt.FakeChannel{fakeChannel()})
	ctx := context.Background()
	if err := registry.AddInput(ctx, r.pool, registry.Input{Input: "@fakecollect", Tier: policy.A}); err != nil {
		t.Fatal(err)
	}
	r.work(t, 3*time.Second)

	if n := r.count(t, `SELECT count(*) FROM channels WHERE id = $1 AND status = 'listed' AND country = 'Iraq' AND subscribers_min = 25000
		AND backfill_done_at IS NOT NULL`, chID); n != 1 {
		t.Fatal("the channel should be listed, read and backfilled")
	}
	if n := r.count(t, `SELECT count(*) FROM channel_snapshots WHERE channel_id = $1`, chID); n != 1 {
		t.Errorf("channel snapshots: %d", n)
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE channel_id = $1`, chID); n != 3 {
		t.Errorf("videos stored: %d; want the 3 inside the 365-day window", n)
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = 'fakeCV3' AND kind = 'short'`); n != 1 {
		t.Error("the Short must be stored as a short")
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = 'fakeCV1' AND relevant AND views = 1200 AND published_precision_s = 0
		AND last_checked IS NOT NULL AND NOT player_unavailable`); n != 1 {
		t.Error("the Baghdad video should be read in full, relevant, with the exact date")
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = 'fakeCV2' AND NOT relevant`); n != 1 {
		t.Error("the recipe is not relevant")
	}
	if n := r.count(t, `SELECT count(*) FROM video_snapshots`); n != 3 {
		t.Errorf("video snapshots: %d; want one per video check", n)
	}
	// Next checks: the new video at A's 30-minute pace with a P1 comment scan;
	// older ones later, with comment scans at P3. The input is done.
	var due time.Time
	_ = r.pool.QueryRow(ctx, `SELECT due_at FROM schedule WHERE kind = 'video' AND entity_id = 'fakeCV1'`).Scan(&due)
	if d := time.Until(due); d < 25*time.Minute || d > 31*time.Minute {
		t.Errorf("the 2-hour-old video's next check is in %v; want about 30 minutes", d)
	}
	if n := r.count(t, `SELECT count(*) FROM schedule WHERE kind = 'comments' AND entity_id = 'fakeCV1' AND priority = 1`); n != 1 {
		t.Error("the new video needs a P1 comment scan")
	}
	if n := r.count(t, `SELECT count(*) FROM schedule WHERE kind = 'comments' AND priority = 3`); n < 1 {
		t.Error("backfilled videos get P3 comment scans")
	}
	if n := r.count(t, `SELECT count(*) FROM schedule WHERE kind IN ('resolve', 'backfill')`); n != 0 {
		t.Error("one-off jobs must be gone")
	}
	if n := r.count(t, `SELECT count(*) FROM channel_inputs WHERE input = '@fakecollect' AND channel_id = $1`, chID); n != 1 {
		t.Error("the input should record what it resolved to")
	}
}

func TestUnknownHandleAndErasedChannel(t *testing.T) {
	r := newRig(t, []fakeyt.FakeChannel{fakeChannel()})
	ctx := context.Background()
	if err := r.sup.Add(ctx, r.pool, privacy.Channel, chID, "erasure request", nil); err != nil {
		t.Fatal(err)
	}
	_ = registry.AddInput(ctx, r.pool, registry.Input{Input: "@fakecollect"})
	_ = registry.AddInput(ctx, r.pool, registry.Input{Input: "@nosuchfake"})
	r.work(t, 2*time.Second)
	if n := r.count(t, `SELECT count(*) FROM channels`); n != 0 {
		t.Error("an erased channel must not be listed again")
	}
	if n := r.count(t, `SELECT count(*) FROM channel_inputs WHERE error IS NOT NULL`); n != 2 {
		t.Errorf("both inputs should record why they failed (%d do)", n)
	}
	if n := r.count(t, `SELECT count(*) FROM schedule`); n != 0 {
		t.Errorf("%d jobs left; failed inputs must not retry forever", n)
	}
}

func TestTabCheckQueuesOnlyNewUploads(t *testing.T) {
	c := fakeChannel()
	r := newRig(t, []fakeyt.FakeChannel{c})
	ctx := context.Background()
	if err := registry.List(ctx, r.pool, nil, chID, registry.Input{Input: chID, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	_, _ = r.pool.Exec(ctx, `UPDATE channels SET backfill_done_at = now() WHERE id = $1`, chID)
	_, _ = r.pool.Exec(ctx, `DELETE FROM schedule WHERE kind <> 'tabs'`)
	_, _ = r.pool.Exec(ctx, `UPDATE schedule SET due_at = now() - interval '1 minute'`)
	// fakeCV2 is already known; the tab check must queue the others at P0.
	_, _ = r.pool.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, title) VALUES ('fakeCV2', $1, 'video', 'recipe of the day')`, chID)
	w := &worker.Worker{Pool: r.pool, Owner: "t", Handlers: map[string]worker.Handler{queue.KindTabs: r.svc.Handlers()[queue.KindTabs]},
		Concurrency: 1, Idle: 10 * time.Millisecond, Log: quiet}
	wctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_ = w.Run(wctx)
	if n := r.count(t, `SELECT count(*) FROM schedule WHERE kind = 'video' AND priority = 0`); n != 2 {
		t.Errorf("%d new uploads queued at P0; want fakeCV1 and the Short", n)
	}
	if n := r.count(t, `SELECT count(*) FROM schedule WHERE kind = 'video' AND entity_id = 'fakeCV2'`); n != 0 {
		t.Error("a known video must not be queued again by the tab check")
	}
}
