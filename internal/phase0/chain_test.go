package phase0

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// stickyRig is a fake YouTube that only honours a continuation token through
// the proxy that received it, behind two real local forward proxies. The
// empty pages of the live crawl on 1 November 2026 turned out to be a wrong
// token (see ytparse.lastContinuation), not this; keeping a chain on one
// visitor, as a browser does, stays as a cheap precaution.
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

// TestSweepLimitZeroReadsAll: 0 means every comment and reply; a positive
// limit stops there and marks the sweep capped.
func TestSweepLimitZeroReadsAll(t *testing.T) {
	_, col := stickyRig(t, fakeChannels())
	var cost Cost
	vr, err := col.Video(context.Background(), "fakeV1", false, &cost)
	if err != nil {
		t.Fatal(err)
	}
	all, err := col.Sweep(context.Background(), "fakeV1", vr.Watch.ChannelID, vr.Watch.CommentsToken, vr.Proxy, 0, &cost)
	if err != nil || all.TopLevel != 3 || all.Replies != 1 || all.Capped {
		t.Fatalf("limit 0: %d comments, %d replies, capped %v, err %v; want 3, 1, not capped", all.TopLevel, all.Replies, all.Capped, err)
	}
	some, err := col.Sweep(context.Background(), "fakeV1", vr.Watch.ChannelID, vr.Watch.CommentsToken, vr.Proxy, 2, &cost)
	if err != nil || some.TopLevel+some.Replies != 2 || !some.Capped {
		t.Fatalf("limit 2: %d read, capped %v, err %v; want 2 and capped", some.TopLevel+some.Replies, some.Capped, err)
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
	// A failure says what each step got back, so the log explains it.
	if _, err := col.Resolve(context.Background(), "@doesnotexist", &cost); err == nil ||
		!strings.Contains(err.Error(), "resolve_url answered no endpoint") || !strings.Contains(err.Error(), "channel page: innertube: not found") {
		t.Errorf("unknown handle: err = %v; want both steps' outcomes", err)
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

// TestShortsWindowUsesPlayerDate: Shorts carry no date on the tab, so the
// last Short of each page is dated through the player and the listing stops
// at the page that crosses the window instead of reading every page.
func TestShortsWindowUsesPlayerDate(t *testing.T) {
	recent := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	old := time.Now().Add(-400 * 24 * time.Hour).Format(time.RFC3339)
	var vs []fakeyt.FakeVideo
	for i := 0; i < 7; i++ {
		pd := recent
		if i >= 3 {
			pd = old
		}
		vs = append(vs, fakeyt.FakeVideo{ID: fmt.Sprintf("fakeS%02d", i), Title: "s", Views: 5, Short: true, PublishDate: pd})
	}
	ch := fakeyt.FakeChannel{ID: chOne, Handle: "@fakeone", Title: "c", Subscribers: "1 subscriber", Videos: vs}
	_, col := stickyRig(t, []fakeyt.FakeChannel{ch})
	var cost Cost
	res, err := col.Tab(context.Background(), chOne, "TAB_shorts", time.Now().Add(-365*24*time.Hour), 10, &cost)
	if err != nil {
		t.Fatal(err)
	}
	// Pages of 2: [s0 s1] ends recent, [s2 s3] ends old, so stop there.
	if res.Pages != 2 || len(res.Items) != 4 || res.DateProbes != 2 || cost.ByEndpoint["player:date"] != 2 {
		t.Fatalf("pages %d items %d probes %d (cost %v); want 2 pages, 4 items, 2 probes", res.Pages, len(res.Items), res.DateProbes, cost.ByEndpoint)
	}
	// Regular videos are dated by the grid itself: no player requests.
	ch.Videos = manyVideos(5)
	_, col = stickyRig(t, []fakeyt.FakeChannel{ch})
	cost = Cost{}
	res, err = col.Tab(context.Background(), chOne, "TAB_videos", time.Now().Add(-365*24*time.Hour), 10, &cost)
	if err != nil || len(res.Items) != 5 || res.DateProbes != 0 || res.Undated != 0 {
		t.Fatalf("videos: %d items, %d probes, %d undated, err %v; want 5, 0, 0", len(res.Items), res.DateProbes, res.Undated, err)
	}
}

// TestResolveFollowsMovedHandle: on 2 November 2026 /@TRTArabi answered
// resolve_url with a urlEndpoint to /trtarabi and its page with a 303 there.
// Either pointer is followed once to the page that names the channel.
func TestResolveFollowsMovedHandle(t *testing.T) {
	legacyPage := `<html><head><link rel="canonical" href="https://www.youtube.com/channel/` + chOne + `"></head></html>`
	t.Run("resolve_url points to the old custom URL", func(t *testing.T) {
		yt, col := stickyRig(t, fakeChannels())
		yt.Handle("navigation/resolve_url", func(body map[string]any) (int, any) {
			u, _ := body["url"].(string)
			if strings.HasSuffix(u, "/@moved") {
				u = "https://www.youtube.com/legacyone"
			}
			return 200, map[string]any{"endpoint": map[string]any{"urlEndpoint": map[string]any{"url": u},
				"commandMetadata": map[string]any{"webCommandMetadata": map[string]any{"webPageType": "WEB_PAGE_TYPE_UNKNOWN"}}}}
		})
		yt.SetRedirect("/@moved", "https://www.youtube.com/legacyone")
		yt.SetPage("/legacyone", legacyPage)
		var cost Cost
		id, err := col.Resolve(context.Background(), "https://www.youtube.com/@moved", &cost)
		if err != nil || id != chOne {
			t.Fatalf("Resolve = %q, %v; want %s", id, err, chOne)
		}
		// The handle's own page would only redirect, so it is skipped.
		if cost.ByEndpoint["resolve"] != 2 || cost.ByEndpoint["page:channel"] != 1 {
			t.Errorf("cost %v; want 2 resolve_url calls and 1 page", cost.ByEndpoint)
		}
	})
	t.Run("only the page redirects", func(t *testing.T) {
		yt, col := stickyRig(t, fakeChannels())
		yt.Handle("navigation/resolve_url", func(map[string]any) (int, any) {
			return 200, map[string]any{"endpoint": map[string]any{}}
		})
		yt.SetRedirect("/@moved", "https://www.youtube.com/legacyone")
		yt.SetPage("/legacyone", legacyPage)
		var cost Cost
		if id, err := col.Resolve(context.Background(), "@moved", &cost); err != nil || id != chOne {
			t.Fatalf("Resolve = %q, %v; want %s", id, err, chOne)
		}
	})
	t.Run("a redirect loop ends with the reasons", func(t *testing.T) {
		yt, col := stickyRig(t, fakeChannels())
		yt.Handle("navigation/resolve_url", func(map[string]any) (int, any) {
			return 200, map[string]any{"endpoint": map[string]any{}}
		})
		yt.SetRedirect("/@a", "https://www.youtube.com/@b")
		yt.SetRedirect("/@b", "https://www.youtube.com/@a")
		var cost Cost
		_, err := col.Resolve(context.Background(), "@a", &cost)
		if err == nil || !strings.Contains(err.Error(), "following to /@b") || cost.Requests > 4 {
			t.Fatalf("err = %v after %d requests; want a bounded failure naming the hop", err, cost.Requests)
		}
	})
	t.Run("pointers off youtube.com are not followed", func(t *testing.T) {
		yt, col := stickyRig(t, fakeChannels())
		yt.Handle("navigation/resolve_url", func(map[string]any) (int, any) {
			return 200, map[string]any{"endpoint": map[string]any{"urlEndpoint": map[string]any{"url": "https://evilyoutube.com/x"}}}
		})
		var cost Cost
		if _, err := col.Resolve(context.Background(), "@nowhere", &cost); err == nil || strings.Contains(err.Error(), "following") {
			t.Fatalf("err = %v; want a failure without following", err)
		}
	})
}

func TestResolveListSaysWhy(t *testing.T) {
	_, col := stickyRig(t, fakeChannels())
	var out strings.Builder
	ok := ResolveList(context.Background(), &out, col, []string{"@fakeone", "", chTwo, "@doesnotexist"})
	s := out.String()
	if ok || !strings.Contains(s, "found      @fakeone  ->  "+chOne) || !strings.Contains(s, "NOT FOUND  @doesnotexist") ||
		!strings.Contains(s, "Resolved 2 of 3 channel(s).") {
		t.Errorf("ok=%v output:\n%s", ok, s)
	}
}

// TestBlockedPlayerNeverStallsTheListing: when the player endpoint is
// blocked, the Shorts date check gives up at once, later checks are skipped
// while it rests, and the listing carries on page by page. Before, a blocked
// check waited for the proxy cooldown (30 minutes or more).
func TestBlockedPlayerNeverStallsTheListing(t *testing.T) {
	var vs []fakeyt.FakeVideo
	for i := 0; i < 7; i++ {
		vs = append(vs, fakeyt.FakeVideo{ID: fmt.Sprintf("fakeS%02d", i), Title: "s", Views: 5, Short: true})
	}
	ch := fakeyt.FakeChannel{ID: chOne, Handle: "@fakeone", Title: "c", Subscribers: "1 subscriber", Videos: vs}
	yt, col := stickyRig(t, []fakeyt.FakeChannel{ch})
	yt.Handle("player", func(map[string]any) (int, any) { return 429, map[string]any{} })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cost Cost
	start := time.Now()
	res, err := col.Tab(ctx, chOne, "TAB_shorts", time.Now().Add(-365*24*time.Hour), 10, &cost)
	if err != nil || len(res.Items) != 7 || res.Pages != 4 {
		t.Fatalf("Tab: %d items over %d pages, err %v; want all 7 over 4 pages", len(res.Items), res.Pages, err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("listing took %v; a blocked player must not hold it up", took)
	}
	if cost.ByEndpoint["player:date:skipped"] != 2 {
		t.Errorf("cost %v; want the first check to try and the next two to be skipped", cost.ByEndpoint)
	}
	// The in-depth video check reports the skipped player instead of waiting.
	vr, err := col.Video(ctx, "fakeS00", true, &cost)
	if err != nil || vr.PlayerErr != errPlayerResting.Error() {
		t.Errorf("Video: PlayerErr %q, err %v; want the resting notice", vr.PlayerErr, err)
	}
}

func TestReportCountsEachRunOnce(t *testing.T) {
	snap := func(run string, at int, requests int64) PoolSnapshot {
		return PoolSnapshot{Run: run, At: time.Unix(int64(at), 0), Stats: proxypool.Stats{
			Proxies: []proxypool.ProxyStats{{ID: "px-a", Requests: requests}}}}
	}
	// Run "a" saved twice (cumulative), run "b" once, plus an old unlabelled one.
	got := latestPerRun([]PoolSnapshot{snap("a", 1, 10), snap("b", 2, 5), snap("a", 3, 25), snap("", 4, 7)})
	var total int64
	for _, ps := range got {
		total += ps.Stats.Proxies[0].Requests
	}
	if len(got) != 3 || total != 25+5+7 {
		t.Errorf("latestPerRun kept %d snapshots totalling %d requests; want 3 and 37", len(got), total)
	}
}

// TestTabSightingNeedsARecentUpload: a tab check counts a video as a new
// upload only when it was published in the last 48 hours. Older videos it
// reaches for the first time (a second page) and undated Shorts are marked
// seen without a sighting.
func TestTabSightingNeedsARecentUpload(t *testing.T) {
	ch := fakeyt.FakeChannel{ID: chOne, Handle: "@fakeone", Title: "c", Subscribers: "1 subscriber", Videos: []fakeyt.FakeVideo{
		{ID: "fakeNew01", Title: "new", Views: 1, Ago: "1 hour ago", LengthSec: 60},
		{ID: "fakeOld01", Title: "older", Views: 1, Ago: "3 days ago", LengthSec: 60},
		{ID: "fakeSht01", Title: "short", Views: 1, Short: true},
	}}
	_, col := stickyRig(t, []fakeyt.FakeChannel{ch})
	st := detectState{Seen: map[string]bool{}, Primed: map[string]bool{chOne: true}}
	checkTab(context.Background(), col, chOne, &st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got, _ := ReadAll[SightingRecord](col.Store, "detect_sightings")
	if len(got) != 1 || got[0].Layer != "tab" || got[0].VideoHash != shortHash("fakeNew01") || got[0].Published.IsZero() {
		t.Fatalf("sightings %+v; want one tab sighting for the 1-hour-old video, with its publish time", got)
	}
	for _, id := range []string{"fakeNew01", "fakeOld01", "fakeSht01"} {
		if !st.Seen["tab:"+id] {
			t.Errorf("%s should be marked seen", id)
		}
	}

	// The report ignores tab sightings recorded without a publish time
	// (before this rule) and counts the rest.
	_ = col.Store.Append("detect_sightings", SightingRecord{At: time.Now(), Layer: "tab"})
	_ = col.Store.Append("detect_feeds", FeedRecord{At: time.Now(), Outcome: "ok"})
	var rep strings.Builder
	if err := Report(col.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.String(), "| New uploads first seen by RSS / by tab check | 0 / 1 |") {
		t.Errorf("report:\n%s", rep.String())
	}
}
