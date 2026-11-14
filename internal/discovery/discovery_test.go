package discovery_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/discovery"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/keywords"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
)

const (
	listed  = "UCfakeDISCOlisted0000001"
	chCore  = "UCfakeDISCOcore000000001"
	chMent  = "UCfakeDISCOmention000001"
	chNone  = "UCfakeDISCOnomatch000001"
	chErase = "UCfakeDISCOerased0000001"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func videos(prefix string, titles ...string) []fakeyt.FakeVideo {
	var vs []fakeyt.FakeVideo
	for i, t := range titles {
		vs = append(vs, fakeyt.FakeVideo{ID: prefix + string(rune('a'+i)), Title: t, Views: 1, Ago: "2 days ago", LengthSec: 60})
	}
	return vs
}

func rig(t *testing.T) (*pgxpool.Pool, *discovery.Runner) {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	yt.ServeChannels([]fakeyt.FakeChannel{
		{ID: chCore, Handle: "@fakecore", Title: "Daily", Country: "Iraq", Subscribers: "250K subscribers", Videos: videos("fakeDc", "x", "y")},
		{ID: chMent, Handle: "@fakemention", Title: "World", Subscribers: "2M subscribers",
			Videos: videos("fakeDm", "football", "music", "travel to Basra", "cars", "phones")},
		{ID: chNone, Handle: "@fakenone", Title: "Kitchen", Subscribers: "10K subscribers", Videos: videos("fakeDn", "pasta", "bread")},
	}, 30, 20)
	yt.ServeSearch(map[string][]fakeyt.FakeSearchHit{
		"العراق": {
			{VideoID: "fakeHL1", ChannelID: listed, Title: "العراق اليوم", Ago: "1 hour ago", Published: time.Now().Add(-time.Hour)},
			{VideoID: "fakeHC1", ChannelID: chCore, Title: "x", Ago: "1 day ago", Published: time.Now().Add(-24 * time.Hour)},
			{VideoID: "fakeHM1", ChannelID: chMent, Title: "travel to Basra", Ago: "2 days ago", Published: time.Now().Add(-48 * time.Hour)},
			{VideoID: "fakeHN1", ChannelID: chNone, Title: "pasta night", Ago: "2 days ago", Published: time.Now().Add(-48 * time.Hour)},
			{VideoID: "fakeHE1", ChannelID: chErase, Title: "العراق", Ago: "2 days ago", Published: time.Now().Add(-48 * time.Hour)},
		},
	}, 3)
	px := fakeyt.NewForwardProxy("a")
	t.Cleanup(px.Close)
	ps, _ := proxypool.ParseList([]string{px.ProxyLine()})
	pp := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true}, 1)
	debug, _ := phase0.OpenStore(t.TempDir())
	pool := dbtest.New(t)
	ctx := context.Background()
	_, _ = kwstore.ImportSeed(ctx, pool)
	live := &kwstore.Live{}
	_, _ = live.Refresh(ctx, pool)
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	_ = sup.Add(ctx, pool, privacy.Channel, chErase, "erasure request", nil)
	_ = registry.List(ctx, pool, nil, listed, registry.Input{Input: listed, Tier: policy.B})
	ledger, _ := ytapi.NewLedger("", 1<<30)
	r := &discovery.Runner{Pool: pool, Sup: sup, Log: quiet,
		Fetch: &phase0.Collector{Client: innertube.New(innertube.Options{Pool: pp, BaseURL: yt.URL}), Store: debug, Now: time.Now, Log: quiet},
		API:   ytapi.New("fake-key", yt.URL+"/youtube/v3", ledger),
		Write: &writer.Writer{Pool: pool, Sup: sup, KW: live}}
	return pool, r
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestQuotaCeilingHolds(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Now()
	got := 0
	for i := 0; i < 5; i++ {
		ok, err := discovery.Reserve(ctx, pool, now, 100, 300)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			got++
		}
	}
	if got != 3 {
		t.Errorf("%d reservations of 100 under a 300 ceiling; want 3", got)
	}
	if ok, _ := discovery.Reserve(ctx, pool, now.Add(24*time.Hour), 100, 300); !ok {
		t.Error("the next quota day starts afresh")
	}
}

func TestSearchFindsCandidatesAndListsThem(t *testing.T) {
	pool, r := rig(t)
	ctx := context.Background()
	added, _, err := discovery.SyncTerms(ctx, pool)
	if err != nil || added < 40 {
		t.Fatalf("sync: %d terms, err %v", added, err)
	}
	term := keywords.Normalize("العراق")
	next, err := r.Handlers()[queue.KindSearch](ctx, queue.Job{Kind: queue.KindSearch, EntityID: term})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(next); d < 55*time.Minute || d > 61*time.Minute {
		t.Errorf("a priority keyword is searched again in %v; want 1 hour", d)
	}
	if n := count(t, pool, `SELECT count(*) FROM search_hits WHERE engine = 'web'`); n != 4 {
		t.Errorf("website hits: %d; the erased channel's must be dropped", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM search_hits WHERE engine = 'api'`); n != 3 {
		t.Errorf("API hits: %d; one API page (the fake pages 3 at a time)", n)
	}
	if n := count(t, pool, `SELECT units_used FROM quota_ledger`); n != 100 {
		t.Errorf("quota used: %d units; one API search costs 100", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = 'video' AND entity_id = 'fakeHL1' AND priority = 1`); n != 1 {
		t.Error("a listed channel's new upload seen in search is queued")
	}
	if n := count(t, pool, `SELECT count(*) FROM candidates`); n != 3 {
		t.Errorf("candidates: %d; want the 3 unlisted, not erased channels", n)
	}
	// A second run soon after: no API call (3-hour window), nothing new.
	if _, err := r.Handlers()[queue.KindSearch](ctx, queue.Job{Kind: queue.KindSearch, EntityID: term}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT units_used FROM quota_ledger`); n != 100 {
		t.Errorf("the API was asked again within 3 hours (%d units)", n)
	}

	for _, id := range []string{chCore, chMent, chNone} {
		if _, err := r.Handlers()[queue.KindCandidate](ctx, queue.Job{Kind: queue.KindCandidate, EntityID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM channels WHERE id = $1 AND kind = 'core' AND tier = 'B' AND added_by = 'discovery'`, chCore); n != 1 {
		t.Error("the Iraq channel is core, tier B by its 250K subscribers")
	}
	// D-076: discovered channels are treated as listed ones, so even the
	// channel with one Basra video in five is core, tiered by its size, with
	// its backfill and tab checks.
	if n := count(t, pool, `SELECT count(*) FROM channels WHERE id = $1 AND kind = 'core' AND tier <> 'M' AND added_by = 'discovery'`, chMent); n != 1 {
		t.Error("the channel with one Basra video in five is a core channel (D-076)")
	}
	for _, k := range []string{queue.KindBackfill, queue.KindTabs} {
		if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = $1 AND entity_id = $2`, k, chMent); n != 1 {
			t.Errorf("the discovered channel has no %s job", k)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM channels WHERE id = $1`, chNone); n != 0 {
		t.Error("the kitchen channel has no matching item: not listed")
	}
	if n := count(t, pool, `SELECT count(*) FROM candidates WHERE channel_id = $1 AND status = 'no_match'`, chNone); n != 1 {
		t.Error("the no-match candidate is remembered")
	}
	if n := count(t, pool, `SELECT count(*) FROM channels WHERE id = $1`, chErase); n != 0 {
		t.Error("an erased channel must never be listed")
	}

	// API hits go after 30 days; website hits stay.
	if n, err := discovery.PurgeAPIHits(ctx, pool, time.Now().Add(31*24*time.Hour)); err != nil || n != 3 {
		t.Errorf("purged %d (err %v)", n, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM search_hits`); n != 4 {
		t.Error("website hits are kept")
	}

	// A removed keyword's search jobs go.
	_, _ = pool.Exec(ctx, `UPDATE keywords SET active = false`)
	if _, removed, err := discovery.SyncTerms(ctx, pool); err != nil || removed < 40 {
		t.Errorf("removed %d terms (err %v)", removed, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = 'search'`); n != 0 {
		t.Error("search jobs of inactive keywords must go")
	}
}

func TestTiers(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	cases := []struct {
		subs   int64
		newest time.Time
		want   policy.Tier
	}{
		{2_000_000, now.Add(-day), policy.A},
		{150_000, now.Add(-day), policy.B},
		{900, now.Add(-day), policy.C},
		{2_000_000, now.Add(-200 * day), policy.D},
		{500, time.Time{}, policy.C},
	}
	for _, c := range cases {
		if got := discovery.TierFor(c.subs, c.newest, now); got != c.want {
			t.Errorf("TierFor(%d, %v) = %s; want %s", c.subs, c.newest, got, c.want)
		}
	}
	pool := dbtest.New(t)
	ctx := context.Background()
	_ = registry.List(ctx, pool, nil, chCore, registry.Input{Input: chCore, Tier: policy.C})
	_ = registry.List(ctx, pool, nil, chMent, registry.Input{Input: chMent, Tier: policy.C})
	_ = registry.Set(ctx, pool, chMent, registry.Core, policy.C) // set by hand
	_, _ = pool.Exec(ctx, `UPDATE channels SET subscribers_min = 3000000`)
	if moved, err := discovery.RecomputeTiers(ctx, pool, now); err != nil || moved != 1 {
		t.Errorf("moved %d (err %v); the hand-set tier must stay", moved, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM channels WHERE id = $1 AND tier = 'A'`, chCore); n != 1 {
		t.Error("3M subscribers: tier A")
	}
}

// REQ-010 fix 10: data first learned from the official API is handled by
// the 30-day rule; data read from the website is never touched.
func TestExpireAPIData(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Now()
	old, recent, mid := now.Add(-31*24*time.Hour), now.Add(-2*24*time.Hour), now.Add(-21*24*time.Hour)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Videos: an old API row never read by scraping, an old API row that was
	// read, a recent API row, and an old website row.
	for _, v := range []struct {
		id, source string
		first      time.Time
		read       bool
	}{
		{"fakeApiOld", "api_search", old, false},
		{"fakeApiRead", "api_search", old, true},
		{"fakeApiNew", "api_search", recent, false},
		{"fakeWebOld", "web_search", old, false},
	} {
		var checked *time.Time
		if v.read {
			checked = &now
		}
		exec(`INSERT INTO videos (id, channel_id, kind, title, published_at, published_precision_s, first_seen, last_checked, source)
			VALUES ($1, 'UCfake0000000000000001', 'video', 'example title', $2, 86400, $2, $3, $4)`, v.id, v.first, checked, v.source)
	}
	// Candidates: API-only unchecked (old, mid, recent), API-only checked,
	// and one also found by website search.
	for _, c := range []struct {
		id      string
		first   time.Time
		sources string
		status  string
	}{
		{"UCfakeCandOld000000000001", old, "{api_search}", "new"},
		{"UCfakeCandMid000000000001", mid, "{api_search}", "new"},
		{"UCfakeCandNew000000000001", recent, "{api_search}", "new"},
		{"UCfakeCandChk000000000001", old, "{api_search}", "checked"},
		{"UCfakeCandWeb000000000001", old, "{api_search,web_search}", "new"},
	} {
		exec(`INSERT INTO candidates (channel_id, first_found, last_found, sources, status) VALUES ($1, $2, $2, $3, $4)`,
			c.id, c.first, c.sources, c.status)
		exec(`INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at) VALUES ('candidate', $1, $1, 3, now() + interval '1 day')`, c.id)
	}
	// Their outbox events (fix 12), as discovery writes them.
	cands := []string{"UCfakeCandOld000000000001", "UCfakeCandMid000000000001", "UCfakeCandNew000000000001",
		"UCfakeCandChk000000000001", "UCfakeCandWeb000000000001"}
	if _, err := outbox.New(ctx, now).Content(ctx, pool, outbox.Candidates, cands...); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO search_hits (engine, video_id, channel_id, title, found_at) VALUES
		('api', 'fakeApiOld', 'UCfake0000000000000001', 'example title', $1),
		('api', 'fakeApiNew', 'UCfake0000000000000001', 'example title', $2),
		('web', 'fakeWebOld', 'UCfake0000000000000001', 'example title', $1)`, old, recent)

	e, err := discovery.ExpireAPIData(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if e != (discovery.APIExpiry{Hits: 1, Videos: 1, Candidates: 1, Prioritised: 1}) {
		t.Errorf("expiry %+v; want 1 hit, 1 video, 1 candidate removed, 1 moved up", e)
	}
	title := func(id string) *string {
		var s *string
		_ = pool.QueryRow(ctx, `SELECT title FROM videos WHERE id = $1`, id).Scan(&s)
		return s
	}
	if title("fakeApiOld") != nil {
		t.Error("an old API title the website never replaced must be cleared")
	}
	for _, id := range []string{"fakeApiRead", "fakeApiNew", "fakeWebOld"} {
		if title(id) == nil {
			t.Errorf("%s: must keep its title (read by scraping, recent, or from the website)", id)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM videos WHERE id = 'fakeApiOld'`).Scan(&n)
	if n != 1 {
		t.Error("the video row stays, with its ID, for a later website read")
	}
	exists := func(table, col, id string) bool {
		var ok bool
		_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE `+col+` = $1)`, id).Scan(&ok)
		return ok
	}
	if exists("candidates", "channel_id", "UCfakeCandOld000000000001") || exists("schedule", "entity_id", "UCfakeCandOld000000000001") {
		t.Error("an API-only candidate unchecked after 30 days must go, with its job")
	}
	if exists("event_outbox", "entity_id", "UCfakeCandOld000000000001") || exists("outbox_state", "entity_id", "UCfakeCandOld000000000001") {
		t.Error("its unsent outbox event and state must go too: nothing from the API outlives 30 days")
	}
	if !exists("event_outbox", "entity_id", "UCfakeCandWeb000000000001") {
		t.Error("the other candidates' events stay")
	}
	for _, id := range []string{"UCfakeCandMid000000000001", "UCfakeCandNew000000000001", "UCfakeCandChk000000000001", "UCfakeCandWeb000000000001"} {
		if !exists("candidates", "channel_id", id) {
			t.Errorf("%s: must stay", id)
		}
	}
	var prio int
	_ = pool.QueryRow(ctx, `SELECT priority FROM schedule WHERE entity_id = 'UCfakeCandMid000000000001'`).Scan(&prio)
	if prio != 1 {
		t.Errorf("a 21-day-old API-only check has priority %d; want 1 (checked before day 30)", prio)
	}
	_ = pool.QueryRow(ctx, `SELECT priority FROM schedule WHERE entity_id = 'UCfakeCandNew000000000001'`).Scan(&prio)
	if prio != 3 {
		t.Errorf("a recent check was moved (priority %d)", prio)
	}
	// Run again: nothing more to do.
	if e, err := discovery.ExpireAPIData(ctx, pool, now); err != nil || e != (discovery.APIExpiry{}) {
		t.Errorf("second run %+v, %v; want nothing", e, err)
	}
}
