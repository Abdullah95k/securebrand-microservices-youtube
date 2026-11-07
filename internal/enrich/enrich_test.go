package enrich_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/enrich"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

const (
	uploader = "UCfakeENRICHuploader0001"
	quietOne = "UCfakeENRICHquiet0000001"
	erased   = "UCfakeENRICHerased000001"
)

func TestEnrichProfilesAndCandidates(t *testing.T) {
	yt := fakeyt.New()
	defer yt.Close()
	yt.ServeChannels([]fakeyt.FakeChannel{
		{ID: uploader, Handle: "@fakeuploader", Title: "Uploader", Country: "Iraq", Subscribers: "3.4K subscribers",
			Videos: []fakeyt.FakeVideo{{ID: "fakeEnV1", Title: "v", Ago: "1 day ago", LengthSec: 60}}},
	}, 30, 20)
	px := fakeyt.NewForwardProxy("a")
	defer px.Close()
	ps, _ := proxypool.ParseList([]string{px.ProxyLine()})
	pp := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true}, 1)
	debug, _ := phase0.OpenStore(t.TempDir())
	pool := dbtest.New(t)
	ctx := context.Background()
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	_ = sup.Add(ctx, pool, privacy.Author, erased, "erasure request", nil)
	_, _ = pool.Exec(ctx, `INSERT INTO authors (id, handle, last_seen, relevant_comments) VALUES
		($1, '@fakeuploader', now(), 40), ($2, '@q', now() - interval '400 days', 0), ($3, '@e', now(), 5)`, uploader, quietOne, erased)
	for _, id := range []string{uploader, quietOne, erased} {
		_ = queue.Put(ctx, pool, queue.KindEnrich, id, "", queue.P4, time.Now())
	}
	if n, err := enrich.Prioritise(ctx, pool); err != nil || n != 2 {
		t.Fatalf("prioritised %d (err %v)", n, err)
	}
	var first string
	_ = pool.QueryRow(ctx, `SELECT entity_id FROM schedule WHERE kind = 'enrich' ORDER BY due_at LIMIT 1`).Scan(&first)
	if first != uploader {
		t.Errorf("the most active relevant commenter should come first, got %s", first)
	}

	e := &enrich.Enricher{Pool: pool, Sup: sup, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Fetch: &phase0.Collector{Client: innertube.New(innertube.Options{Pool: pp, BaseURL: yt.URL}), Store: debug, Now: time.Now}}
	run := func(id string) time.Time {
		next, err := e.Handlers()[queue.KindEnrich](ctx, queue.Job{Kind: queue.KindEnrich, EntityID: id})
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	next := run(uploader)
	if d := time.Until(next); d < 170*24*time.Hour || d > 181*24*time.Hour {
		t.Errorf("an active commenter is read again in %v; want about 6 months", d)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM author_profiles WHERE author_id = $1 AND country = 'Iraq' AND subscribers_min = 3400`, uploader).Scan(&n)
	if n != 1 {
		t.Error("the profile should be stored with country and subscribers")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM schedule WHERE kind = 'candidate' AND entity_id = $1`, uploader).Scan(&n)
	if n != 1 {
		t.Error("a commenter with uploads goes to the candidate check")
	}
	// The commenter goes out with the profile (fix 12).
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE kind = 'author' AND entity_id = $1
		AND payload->'profile'->>'country' = 'Iraq'`, uploader).Scan(&n)
	if n != 1 {
		t.Error("the enriched commenter's event must carry the profile")
	}
	if next := run(quietOne); time.Until(next) < 360*24*time.Hour {
		t.Error("a channel that is not found is still recorded, and an inactive commenter waits a year")
	}
	if next := run(erased); !next.IsZero() {
		t.Error("an erased author is never read")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM author_profiles WHERE author_id = $1`, erased).Scan(&n)
	if n != 0 {
		t.Error("no profile for an erased author")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE entity_id IN ($1, $2)`, quietOne, erased).Scan(&n)
	if n != 0 {
		t.Error("no event for a commenter not found, or erased")
	}
}
