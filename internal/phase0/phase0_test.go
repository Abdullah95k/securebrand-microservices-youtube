package phase0

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

const (
	chOne  = "UCfakePHASE0PHASE0CHAN01"
	chTwo  = "UCfakePHASE0PHASE0CHAN02"
	viewer = "UCfakeVIEWERVIEWERVIEWER"
)

func fakeChannels() []fakeyt.FakeChannel {
	comments := []fakeyt.FakeComment{
		{ID: "Ugw1", Text: "zqcommentone", Author: viewer, Ago: "2 hours ago", Likes: "3", Pinned: true, Hearted: true,
			Replies: []fakeyt.FakeComment{{ID: "Ugw1.r1", Text: "reply", Author: chOne, Ago: "1 hour ago", Likes: "0"}}},
		{ID: "Ugw2", Text: "zqcommenttwo", Author: viewer, Ago: "3 days ago", Likes: "1.2K"},
		{ID: "Ugw3", Text: "zqcommentthree", Author: viewer, Ago: "2 weeks ago", Likes: ""},
	}
	return []fakeyt.FakeChannel{
		{ID: chOne, Handle: "@fakeone", Title: "Fake one", Country: "Iraq", Subscribers: "1.2K subscribers", Videos: []fakeyt.FakeVideo{
			{ID: "fakeV1", Title: "بغداد اليوم", Views: 1000, Likes: 50, Ago: "2 days ago", LengthSec: 600, Comments: comments, CommentsTxt: "4"},
			{ID: "fakeV2", Title: "Second", Views: 10, Likes: 1, Ago: "5 months ago", LengthSec: 61, CommentsTxt: "0"},
			{ID: "fakeV3", Title: "Too old", Views: 5, Likes: 0, Ago: "2 years ago", LengthSec: 30, CommentsTxt: "0"},
			{ID: "fakeS1", Title: "Short", Views: 99, Likes: 9, Ago: "1 day ago", Short: true, CommentsTxt: "0"},
		}},
		{ID: chTwo, Handle: "@faketwo", Title: "Fake two", Subscribers: "12 subscribers", Videos: []fakeyt.FakeVideo{
			{ID: "fakeV4", Title: "Only video", Views: 7, Likes: 0, Ago: "1 week ago", LengthSec: 100, CommentsTxt: "0"},
		}},
	}
}

func writeSecret(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestEndToEnd runs crawl, resweep, accuracy, detect and report against the
// fake YouTube through two real (local) forward proxies.
func TestEndToEnd(t *testing.T) {
	yt := fakeyt.New()
	defer yt.Close()
	chans := fakeChannels()
	yt.ServeChannels(chans, 2, 2)
	yt.Handle("api:videos", func(q map[string]any) (int, any) {
		var items []any
		for _, id := range strings.Split(q["id"].(string), ",") {
			items = append(items, map[string]any{"id": id, "snippet": map[string]any{"publishedAt": "2026-10-30T10:00:00Z"},
				"statistics": map[string]any{"viewCount": "1000", "likeCount": "50", "commentCount": "4"}})
		}
		return 200, map[string]any{"items": items}
	})
	yt.Handle("api:videoCategories", func(map[string]any) (int, any) { return 200, map[string]any{"items": []any{}} })
	p1, p2 := fakeyt.NewForwardProxy("a"), fakeyt.NewForwardProxy("b")
	defer p1.Close()
	defer p2.Close()

	cfg := t.TempDir()
	t.Setenv(EnvProxiesFile, writeSecret(t, cfg, "proxies.txt", p1.ProxyLine()+"\n"+p2.ProxyLine()+" type=isp\n"))
	t.Setenv(EnvAPIKeyFile, writeSecret(t, cfg, "key", "fake-key"))
	t.Setenv(EnvChannelsFile, writeSecret(t, cfg, "channels.txt", "# test list\n@fakeone\n"+chTwo+"\n"))
	t.Setenv(EnvDataDir, filepath.Join(t.TempDir(), "data"))
	t.Setenv(EnvBaseURL, yt.URL)

	env, err := Setup(proxypool.Config{MinInterval: time.Millisecond, FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond, TransientBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// Setup check.
	var checkOut bytes.Buffer
	if !Check(ctx, &checkOut, env.Proxies, env.BaseURL, env.API, env.Channels) {
		t.Fatalf("check failed:\n%s", checkOut.String())
	}

	// Crawl (with one block on the way, to exercise failover).
	yt.BlockNext("next", 1, fakeyt.Block429)
	o := DefaultCrawlOptions()
	o.Workers, o.CommentLimit = 2, 100
	if err := Crawl(ctx, env.Collector, env.Channels, o, log); err != nil {
		t.Fatal(err)
	}
	env.SavePoolStats("crawl")
	crs, _ := ReadAll[ChannelRecord](env.Store, "crawl_channels")
	if len(crs) != 2 {
		t.Fatalf("channel records = %d", len(crs))
	}
	for _, c := range crs {
		if c.Error != "" {
			t.Fatalf("channel error: %s", c.Error)
		}
	}
	var one ChannelRecord
	for _, c := range crs {
		if c.ChannelHash == shortHash(chOne) {
			one = c
		}
	}
	if one.VideosInWindow != 2 || one.ShortsInWindow != 1 || !one.Fields["country"] || !one.Fields["links"] || one.Subscribers.Min != 1200 {
		t.Errorf("channel one record: %+v", one)
	}
	vrs, _ := ReadAll[VideoRecord](env.Store, "crawl_videos")
	var v1 VideoRecord
	for _, v := range vrs {
		if v.VideoHash == shortHash("fakeV1") {
			v1 = v
		}
	}
	if v1.Comments.TopLevel != 3 || v1.Comments.Replies != 1 || v1.Comments.Pinned != 1 || v1.Comments.Hearted != 1 || v1.Comments.CreatorReplies != 1 {
		t.Errorf("comment sweep: %+v", v1.Comments)
	}
	if !v1.Fields["tags"] || !v1.Fields["likes_exact"] || !v1.Fields["publish_datetime"] || v1.Comments.LikesAbbreviated != 1 {
		t.Errorf("video fields: %+v likesAbbrev=%d", v1.Fields, v1.Comments.LikesAbbreviated)
	}

	// Change the comments: delete one, edit one, add one; then re-sweep.
	changed := fakeChannels()
	cm := changed[0].Videos[0].Comments
	cm[1].Text = "zqcommenttwo edited"
	changed[0].Videos[0].Comments = append([]fakeyt.FakeComment{{ID: "Ugw9", Text: "new", Author: viewer, Ago: "5 minutes ago", Likes: "0"}}, cm[0], cm[1])
	yt.ServeChannels(changed, 2, 2)
	if err := Resweep(ctx, env.Collector, 100, log); err != nil {
		t.Fatal(err)
	}
	rs, _ := ReadAll[ResweepRecord](env.Store, "resweep")
	var r1 ResweepRecord
	for _, r := range rs {
		if r.VideoHash == shortHash("fakeV1") {
			r1 = r
		}
	}
	if r1.New != 1 || r1.Missing != 1 || r1.Edited != 1 || r1.StopEarlyNew != 1 {
		t.Errorf("resweep: %+v", r1)
	}

	// Accuracy check against the (fake) official API.
	if err := Accuracy(ctx, env.Collector, env.API, 10, log); err != nil {
		t.Fatal(err)
	}
	acc, _ := ReadAll[AccuracyRecord](env.Store, "accuracy")
	if len(acc) == 0 || !acc[0].Found {
		t.Fatalf("accuracy records: %+v", acc)
	}

	// Detection: prime, then publish a new upload and expect a sighting.
	yt.SetFeed(chOne, []fakeyt.FeedEntry{{VideoID: "fakeV1", ChannelID: chOne, Title: "old", Published: time.Now().Add(-48 * time.Hour), Views: 1}})
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Detect(dctx, env.Collector, env.Feeds, []string{chOne}, DetectOptions{RSSEvery: 100 * time.Millisecond, TabEvery: time.Hour, Tick: 20 * time.Millisecond}, log)
	}()
	time.Sleep(300 * time.Millisecond)
	yt.SetFeed(chOne, []fakeyt.FeedEntry{
		{VideoID: "fakeNEW", ChannelID: chOne, Title: "new upload", Published: time.Now().Add(-2 * time.Minute), Views: 3},
		{VideoID: "fakeV1", ChannelID: chOne, Title: "old", Published: time.Now().Add(-48 * time.Hour), Views: 1},
	})
	deadline := time.Now().Add(3 * time.Second)
	var sightings []SightingRecord
	for time.Now().Before(deadline) {
		sightings, _ = ReadAll[SightingRecord](env.Store, "detect_sightings")
		if len(sightings) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if len(sightings) != 1 || sightings[0].Layer != "rss" || sightings[0].DelaySec < 60 {
		t.Fatalf("sightings: %+v", sightings)
	}

	// Report: aggregates only.
	var rep bytes.Buffer
	if err := Report(env.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := rep.String()
	for _, want := range []string{"# Phase 0 results", "Channels crawled", "Videos checked in depth", "Edits and deletions", "New-upload detection", "Accuracy against the official API", "Requests and block signals"} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q", want)
		}
	}
	for _, leak := range []string{chOne, chTwo, viewer, "fakeV1", "بغداد", "zqcomment", "@fakeone"} {
		if strings.Contains(out, leak) {
			t.Errorf("report leaks %q", leak)
		}
	}
	// No comment text or authors are stored anywhere in the data folder.
	_ = filepath.Walk(env.Store.Dir(), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, _ := os.ReadFile(p)
		for _, leak := range []string{"zqcomment", viewer} {
			if bytes.Contains(data, []byte(leak)) {
				t.Errorf("%s stores %q", filepath.Base(p), leak)
			}
		}
		return nil
	})
}

func TestSampleVideos(t *testing.T) {
	if got := sampleVideos(nil, 5); got != nil {
		t.Errorf("empty input: %v", got)
	}
}
