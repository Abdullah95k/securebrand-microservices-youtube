package phase0

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// paceEnv is an Env on a fake YouTube behind two local forward proxies.
func paceEnv(t *testing.T) (*fakeyt.Server, *Env) {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	p1, p2 := fakeyt.NewForwardProxy("a"), fakeyt.NewForwardProxy("b")
	t.Cleanup(p1.Close)
	t.Cleanup(p2.Close)
	ps, err := proxypool.ParseList([]string{p1.ProxyLine(), p2.ProxyLine()})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true,
		FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond, TransientBackoff: time.Millisecond}, 1)
	client := innertube.New(innertube.Options{Pool: pool, BaseURL: yt.URL, CDNBase: yt.URL})
	env := &Env{Store: store, Proxies: ps, Pool: pool, Client: client, BaseURL: yt.URL,
		Collector: &Collector{Client: client, Store: store, Now: time.Now}}
	return yt, env
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// TestPaceStepsDownAndStopsAtFirstBlock: browse starts answering 429 from
// its 13th request, which falls in the third step; that step ends the browse
// test and nothing faster is tried. RSS never blocks and runs every step.
func TestPaceStepsDownAndStopsAtFirstBlock(t *testing.T) {
	yt, env := paceEnv(t)
	var mu sync.Mutex
	browseCalls := 0
	yt.Handle("browse", func(map[string]any) (int, any) {
		mu.Lock()
		defer mu.Unlock()
		browseCalls++
		if browseCalls > 12 {
			return 429, map[string]any{}
		}
		return 200, map[string]any{"contents": map[string]any{}}
	})
	yt.SetFeed(chOne, []fakeyt.FeedEntry{{VideoID: "fakeV1", ChannelID: chOne, Title: "t", Published: time.Now(), Views: 1}})
	o := PaceOptions{
		Classes:         []proxypool.Class{proxypool.ClassBrowse, proxypool.ClassRSS, proxypool.ClassCDN},
		Steps:           []time.Duration{30 * time.Millisecond, 20 * time.Millisecond, 10 * time.Millisecond},
		PerStep:         3,
		Proxies:         2,
		StopAfterBlocks: 1,
	}
	targets := PaceTargets{Channels: []string{chOne}, Videos: []string{"fakeV1"}, Queries: []string{"q"}}
	if err := RunPace(context.Background(), env, targets, o, quietLog); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll[PaceRecord](env.Store, "pace_steps")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string][]PaceRecord{}
	for _, r := range recs {
		by[r.Class] = append(by[r.Class], r)
	}
	b := by["browse"]
	if len(b) != 3 || b[0].Blocks != 0 || b[1].Blocks != 0 || b[2].Blocks < 1 || !b[2].Stopped || b[0].Requests != 6 {
		t.Fatalf("browse steps %+v; want two clean steps of 6 requests, then a stopped one", b)
	}
	mu.Lock()
	calls := browseCalls
	mu.Unlock()
	if calls > 14 { // 12 clean + the blocked one (+ at most one already in flight)
		t.Errorf("browse got %d requests; nothing should be sent after the stop", calls)
	}
	for _, c := range []string{"rss", "cdn"} {
		if len(by[c]) != 3 || by[c][2].Stopped || by[c][2].OK != 6 {
			t.Errorf("%s steps %+v; want all 3 steps clean with 6 answers each", c, by[c])
		}
	}
	if b[0].GapSec != 0.03 || b[0].Kinds != "dc" || b[0].Proxies != 2 {
		t.Errorf("step metadata %+v", b[0])
	}

	var rep strings.Builder
	if err := Report(env.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := rep.String()
	for _, want := range []string{"## Safe pace per endpoint (E1)", "| browse | dc | 0.02 s | 0.01 s |", "| rss | dc | 0.01 s | none up to 0.01 s |"} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	// The pace pool blocks on purpose and must not count as crawl blocks.
	if strings.Contains(out, "Requests and block signals by proxy type") {
		t.Error("the pace pool should not appear in the crawl's block table")
	}
}

func TestGatherPaceTargets(t *testing.T) {
	_, col := stickyRig(t, fakeChannels())
	got, err := GatherPaceTargets(context.Background(), col, []string{"@fakeone", "@doesnotexist", chTwo}, 5, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Channels) != 2 || len(got.Videos) == 0 || len(got.Queries) == 0 {
		t.Errorf("targets %+v; want both real channels, some videos and seed search terms", got)
	}
	for _, q := range got.Queries {
		if q == "" {
			t.Error("empty search term")
		}
	}
}

func TestFailoverSectionUsesWorkPools(t *testing.T) {
	_, env := paceEnv(t)
	snap := func(cmd, run string, cs proxypool.ClassStats) PoolSnapshot {
		return PoolSnapshot{At: time.Now(), Command: cmd, Run: run, Stats: proxypool.Stats{
			Classes: map[proxypool.Class]proxypool.ClassStats{proxypool.ClassBrowse: cs}}}
	}
	_ = env.Store.Append("pool_stats", snap("crawl", "r1", proxypool.ClassStats{BlockedRequests: 4, FailoverOK: 3, Parked: 1, Recovered: 2, Reblocked: 1, RecoverySeconds: []float64{1800, 2400, 3000}}))
	_ = env.Store.Append("pool_stats", snap("pace", "p1", proxypool.ClassStats{BlockedRequests: 9, Parked: 9}))
	var rep strings.Builder
	if err := Report(env.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.String(), "| browse | 4 | 3 (75%) | 1 | 2 / 1 | 40 |") {
		t.Errorf("failover section wrong or includes the pace pool:\n%s", rep.String())
	}
}
