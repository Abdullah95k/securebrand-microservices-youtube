// Package collecttest runs yts end to end against the fake YouTube, for
// tests that need real stored data and outbox events (the contract and
// yt.* mappings, fix 12): one fake channel with videos, a Short, comments
// and a reply, a community post, a playlist and commenters, collected by
// the real worker with the comment engine and enrichment.
package collecttest

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/comments"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/enrich"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
)

// ChannelID is the fake channel's ID.
const ChannelID = "UCfakeCONTRACT0000000001"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// Channel is the fake channel: synthetic IDs and text only.
func Channel() fakeyt.FakeChannel {
	return fakeyt.FakeChannel{ID: ChannelID, Handle: "@fakecontract", Title: "قناة تجريبية", Country: "Iraq", Subscribers: "25K subscribers",
		Videos: []fakeyt.FakeVideo{
			{ID: "fakeKV1", Title: "أخبار بغداد اليوم", Views: 1200, Likes: 40, Ago: "2 hours ago", LengthSec: 300, CommentsTxt: "3",
				PublishDate: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				Comments: []fakeyt.FakeComment{
					{ID: "fakeKC1", Text: "first", Author: "UCfakeKAUTHOR00000000001", Ago: "1 hour ago", Likes: "1.2K", Pinned: true,
						Replies: []fakeyt.FakeComment{{ID: "fakeKC1.r1", Text: "a reply", Author: ChannelID, Ago: "30 minutes ago", Likes: "2"}}},
					{ID: "fakeKC2", Text: "second", Author: "UCfakeKAUTHOR00000000002", Ago: "1 hour ago", Likes: "0"},
				}},
			{ID: "fakeKV2", Title: "recipe of the day", Views: 50, Likes: 2, Ago: "3 days ago", LengthSec: 120, CommentsTxt: "0"},
			{ID: "fakeKV3", Title: "short from Basra", Views: 999, Ago: "5 days ago", LengthSec: 30, Short: true, CommentsTxt: "0"},
		},
		Posts:     []fakeyt.FakePost{{ID: "UgkxFAKEKPOST1", Text: "منشور عن بغداد", Ago: "1 day ago", Likes: "1.2K", CommentsTxt: "0"}},
		Playlists: []fakeyt.FakePlaylist{{ID: "PLfakeKLIST01", Title: "تقارير بغداد", VideoIDs: []string{"fakeKV1", "fakeKV2"}}},
	}
}

// Result is the database after the run, and the keyword matcher.
type Result struct {
	Pool *pgxpool.Pool
	Live *kwstore.Live
}

// Run lists the fake channel and runs the collector, the comment engine
// and enrichment against the fake YouTube (one proxy, country=IQ) for a
// few seconds.
func Run(t testing.TB) Result {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	yt.ServeChannels([]fakeyt.FakeChannel{Channel()}, 2, 2)
	px := fakeyt.NewForwardProxy("a")
	t.Cleanup(px.Close)
	ps, err := proxypool.ParseList([]string{px.ProxyLine() + " country=IQ"})
	if err != nil {
		t.Fatal(err)
	}
	pp := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true,
		FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond, TransientBackoff: time.Millisecond}, 1)
	debug, err := phase0.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := innertube.New(innertube.Options{Pool: pp, BaseURL: yt.URL, CDNBase: yt.URL})
	fetch := &phase0.Collector{Client: client, Store: debug, Now: time.Now, Log: quiet}
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
	h := svc.Handlers()
	for kind, f := range (&comments.Engine{Pool: pool, Client: client, Sup: sup, Log: quiet}).Handlers() {
		h[kind] = f
	}
	for kind, f := range (&enrich.Enricher{Pool: pool, Sup: sup, Log: quiet, Fetch: fetch}).Handlers() {
		h[kind] = f
	}
	if err := registry.AddInput(ctx, pool, registry.Input{Input: "@fakecontract", Tier: policy.A}); err != nil {
		t.Fatal(err)
	}
	w := &worker.Worker{Pool: pool, Owner: "test", Handlers: h, Concurrency: 4, Idle: 10 * time.Millisecond, Lease: time.Minute, Log: quiet}
	wctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	_ = w.Run(wctx)
	return Result{Pool: pool, Live: live}
}
