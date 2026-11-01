package feeds

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

const fakeChannel = "UCfakeRSSRSSRSSRSSRSSRSS"

var published = time.Date(2026, 10, 30, 18, 4, 5, 0, time.UTC)

func entries() []fakeyt.FeedEntry {
	return []fakeyt.FeedEntry{
		{VideoID: "fakevid0001", ChannelID: fakeChannel, Title: "أخبار بغداد & more", Published: published, Views: 12345, Likes: 678},
		{VideoID: "fakevid0002", ChannelID: fakeChannel, Title: "Second", Published: published.Add(-time.Hour), Views: 1, Likes: 0},
	}
}

func TestParse(t *testing.T) {
	f, err := Parse(fakeyt.FeedXML(fakeChannel, entries()))
	if err != nil {
		t.Fatal(err)
	}
	if f.ChannelID != fakeChannel || len(f.Entries) != 2 {
		t.Fatalf("feed = %+v", f)
	}
	e := f.Entries[0]
	if e.VideoID != "fakevid0001" || e.Title != "أخبار بغداد & more" || !e.Published.Equal(published) {
		t.Errorf("entry = %+v", e)
	}
	if !e.HasStats || e.Views != 12345 || e.Likes != 678 {
		t.Errorf("stats = views %d likes %d has %v", e.Views, e.Likes, e.HasStats)
	}
	if e.Description == "" || e.Thumbnail == "" || e.Link == "" {
		t.Errorf("missing fields: %+v", e)
	}
	if _, err := Parse([]byte("not xml <")); err == nil {
		t.Error("want error for invalid XML")
	}
}

func newFetcher(t *testing.T) (*Fetcher, *fakeyt.Server) {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	ps, err := proxypool.ParseList([]string{"direct"})
	if err != nil {
		t.Fatal(err)
	}
	pool := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true,
		FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond, TransientBackoff: time.Millisecond}, 1)
	return NewFetcher(innertube.New(innertube.Options{Pool: pool, BaseURL: yt.URL})), yt
}

func TestFetchConditional(t *testing.T) {
	f, yt := newFetcher(t)
	yt.SetFeed(fakeChannel, entries())
	res, err := f.Fetch(context.Background(), fakeChannel)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Feed.Entries) != 2 || res.WireBytes == 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := f.Fetch(context.Background(), fakeChannel); !errors.Is(err, ErrNotModified) {
		t.Fatalf("second fetch err = %v, want ErrNotModified", err)
	}
	reqs := yt.Requests()
	if last := reqs[len(reqs)-1]; last.Header.Get("If-None-Match") == "" {
		t.Error("second fetch should send If-None-Match")
	}
	// A changed feed is fetched again.
	changed := entries()
	changed[0].Views++
	yt.SetFeed(fakeChannel, changed)
	res, err = f.Fetch(context.Background(), fakeChannel)
	if err != nil || res.Feed.Entries[0].Views != 12346 {
		t.Fatalf("after change: err=%v", err)
	}
}

func TestFetchNotFound(t *testing.T) {
	f, _ := newFetcher(t)
	if _, err := f.Fetch(context.Background(), "UCfakeMISSINGMISSINGMISS"); !errors.Is(err, innertube.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
