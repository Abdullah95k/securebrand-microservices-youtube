package phase0

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// stickyRig is a fake YouTube that only honours a continuation token through
// the proxy that received it (what the live site appeared to do on
// 1 November 2026), behind two real local forward proxies.
func stickyRig(t *testing.T, chans []fakeyt.FakeChannel) (*fakeyt.Server, *Collector) {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	yt.ServeChannels(chans, 2, 2)
	yt.StickyContinuations = true
	p1, p2 := fakeyt.NewForwardProxy("a"), fakeyt.NewForwardProxy("b")
	t.Cleanup(p1.Close)
	t.Cleanup(p2.Close)
	ps, err := proxypool.ParseList([]string{p1.ProxyLine(), p2.ProxyLine()})
	if err != nil {
		t.Fatal(err)
	}
	pool := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true,
		FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond, TransientBackoff: time.Millisecond}, 1)
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	col := &Collector{Client: innertube.New(innertube.Options{Pool: pool, BaseURL: yt.URL}), Store: store, Now: time.Now}
	return yt, col
}

func manyVideos(n int) []fakeyt.FakeVideo {
	var vs []fakeyt.FakeVideo
	for i := 0; i < n; i++ {
		vs = append(vs, fakeyt.FakeVideo{ID: fmt.Sprintf("fakeP%02d", i), Title: "v", Views: 1, Ago: "1 day ago", LengthSec: 60, CommentsTxt: "0"})
	}
	return vs
}

// TestTabPaginationStaysOnOneProxy fails without request chaining: page 2
// would go through the other proxy and come back empty.
func TestTabPaginationStaysOnOneProxy(t *testing.T) {
	ch := fakeyt.FakeChannel{ID: chOne, Handle: "@fakeone", Title: "c", Subscribers: "1 subscriber", Videos: manyVideos(7)}
	_, col := stickyRig(t, []fakeyt.FakeChannel{ch})
	var cost Cost
	res, err := col.Tab(context.Background(), chOne, "TAB_videos", time.Now().Add(-365*24*time.Hour), 10, &cost)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 7 || res.Pages != 4 {
		t.Fatalf("got %d items over %d pages; want all 7 over 4 pages", len(res.Items), res.Pages)
	}
}

// TestCommentSweepStaysOnOneProxy: the comment header, its pages and the
// replies all go through the proxy that read the watch page.
func TestCommentSweepStaysOnOneProxy(t *testing.T) {
	_, col := stickyRig(t, fakeChannels())
	var cost Cost
	vr, err := col.Video(context.Background(), "fakeV1", false, &cost)
	if err != nil {
		t.Fatal(err)
	}
	sw, err := col.Sweep(context.Background(), "fakeV1", vr.Watch.ChannelID, vr.Watch.CommentsToken, vr.Proxy, 100, &cost)
	if err != nil {
		t.Fatal(err)
	}
	if sw.TopLevel != 3 || sw.Replies != 1 {
		t.Fatalf("sweep got %d comments and %d replies; want 3 and 1", sw.TopLevel, sw.Replies)
	}
}

func TestResolveFallsBackToChannelPage(t *testing.T) {
	yt, col := stickyRig(t, fakeChannels())
	// resolve_url answers without a channel ID, as it did for some
	// handles on the live site.
	yt.Handle("navigation/resolve_url", func(map[string]any) (int, any) {
		return 200, map[string]any{"endpoint": map[string]any{"commandMetadata": map[string]any{}}}
	})
	var cost Cost
	for _, in := range []string{"@fakeone", "https://www.youtube.com/@fakeone"} {
		id, err := col.Resolve(context.Background(), in, &cost)
		if err != nil || id != chOne {
			t.Errorf("Resolve(%q) = %q, %v; want %s from the channel page", in, id, err, chOne)
		}
	}
	if cost.ByEndpoint["page:channel"] != 2 {
		t.Errorf("want the channel page fetched once per input, cost %v", cost.ByEndpoint)
	}
	if _, err := col.Resolve(context.Background(), "@doesnotexist", &cost); err == nil {
		t.Error("want an error for an unknown handle")
	}
	// Channel IDs and /channel/ URLs need no request at all.
	before := cost.Requests
	if id, _ := col.Resolve(context.Background(), "https://www.youtube.com/channel/"+chTwo, &cost); id != chTwo || cost.Requests != before {
		t.Errorf("/channel/ URL: id %q, requests %d → %d", id, before, cost.Requests)
	}
}

func TestSkeletonsAreCappedPerKind(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		s.SaveSkeleton(fmt.Sprintf("tabnext-%d", i), []byte(`{"a":1}`))
	}
	s.SaveSkeleton("watch-1", []byte(`{"a":1}`))
	files, _ := os.ReadDir(filepath.Join(s.Dir(), "skeletons"))
	if len(files) != maxSkeletonsPerKind+1 {
		t.Errorf("got %d skeleton files; want %d of one kind plus 1 of another", len(files), maxSkeletonsPerKind)
	}
}
