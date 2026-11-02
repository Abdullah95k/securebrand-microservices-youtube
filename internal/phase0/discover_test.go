package phase0

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

const (
	chNew1 = "UCfakePHASE0PHASE0NEW001" // About country Iraq
	chNew2 = "UCfakePHASE0PHASE0NEW002" // title names Basra
	chNew3 = "UCfakePHASE0PHASE0NEW003" // neither: mention only
)

// discoveryRig: the list holds chOne (already crawled); searches also find
// three channels the list lacks.
func discoveryRig(t *testing.T) (*fakeyt.Server, *Collector, *ytapi.Client) {
	t.Helper()
	vids := func(prefix string) []fakeyt.FakeVideo {
		return []fakeyt.FakeVideo{
			{ID: prefix + "a", Title: "v", Views: 10, Ago: "2 days ago", LengthSec: 60, CommentsTxt: "12"},
			{ID: prefix + "b", Title: "v", Views: 10, Ago: "10 days ago", LengthSec: 60, CommentsTxt: "3"},
			{ID: prefix + "c", Title: "v", Views: 10, Ago: "2 months ago", LengthSec: 60, CommentsTxt: "1"},
		}
	}
	chans := []fakeyt.FakeChannel{
		{ID: chOne, Handle: "@fakeone", Title: "One", Subscribers: "1.2K subscribers", Videos: vids("fakeO1")},
		{ID: chNew1, Handle: "@new1", Title: "Daily news", Country: "Iraq", Subscribers: "25K subscribers", Videos: vids("fakeN1")},
		{ID: chNew2, Handle: "@new2", Title: "قناة البصرة", Country: "", Subscribers: "900 subscribers", Videos: vids("fakeN2")},
		{ID: chNew3, Handle: "@new3", Title: "Brand reviews", Country: "Germany", Subscribers: "2M subscribers", Videos: vids("fakeN3")},
	}
	yt, col := stickyRig(t, chans)
	now := time.Now()
	yt.ServeSearch(map[string][]fakeyt.FakeSearchHit{
		"بغداد": {
			{VideoID: "fakeS1", ChannelID: chOne, Title: "بغداد اليوم", Ago: "1 day ago", Published: now.Add(-24 * time.Hour)},
			{VideoID: "fakeS2", ChannelID: chNew1, Title: "أخبار بغداد", Ago: "2 days ago", Published: now.Add(-48 * time.Hour)},
			{VideoID: "fakeS3", ChannelID: chNew2, Title: "nothing relevant", Ago: "3 days ago", Published: now.Add(-72 * time.Hour)},
		},
		"zqbrandx": {
			{VideoID: "fakeS4", ChannelID: chNew3, Title: "zqbrandx review", Ago: "1 day ago", Published: now.Add(-24 * time.Hour)},
		},
	}, 2)
	// chOne was crawled, so it is in the list.
	if err := col.Store.Append("crawl_channels", ChannelRecord{ChannelHash: shortHash(chOne)}); err != nil {
		t.Fatal(err)
	}
	ledger, _ := ytapi.NewLedger("", ytapi.DefaultDailyQuota)
	return yt, col, ytapi.New("fake-key", yt.URL+"/youtube/v3", ledger)
}

func TestDiscoverComparesEnginesAndFindsNewChannels(t *testing.T) {
	_, col, api := discoveryRig(t)
	o := DefaultDiscoverOptions()
	o.Terms = []string{"بغداد", "zqbrandx"}
	o.APIUnits = 100 // one API search: the second term is website only
	if err := Discover(context.Background(), col, api, o, quietLog); err != nil {
		t.Fatal(err)
	}
	recs, _ := ReadAll[DiscoverRecord](col.Store, "discover")
	get := func(engine, term string) *DiscoverRecord {
		for i := range recs {
			if recs[i].Engine == engine && recs[i].TermHash == shortHash(term) {
				return &recs[i]
			}
		}
		return nil
	}
	w := get("web", "بغداد")
	if w == nil || w.Pages != 2 || !w.RanOut || w.Videos != 3 || w.Channels != 3 || w.NewChannels != 2 || w.NewQualifying != 1 {
		t.Fatalf("web record %+v; want 2 pages that ran out, 3 videos and channels, 2 new of which 1 matches", w)
	}
	a := get("api", "بغداد")
	if a == nil || a.Units != 100 || a.Pages != 1 || a.RanOut || a.Results != 2 {
		t.Fatalf("api record %+v; want one 100-unit page of 2 results with more to come", a)
	}
	if get("api", "zqbrandx") != nil {
		t.Error("the unit budget allowed only one API search")
	}
	if b := get("web", "zqbrandx"); b == nil || b.NewQualifying != 1 {
		t.Errorf("your own term should count as a match: %+v", b)
	}
	// Terms are stored as hashes only.
	data, _ := os.ReadFile(filepath.Join(col.Store.Dir(), "discover.jsonl"))
	if strings.Contains(string(data), "zqbrandx") || strings.Contains(string(data), "بغداد") {
		t.Error("discover records must not contain the search terms")
	}

	// Census: the three new channels, core or mention.
	if err := Census(context.Background(), col, CensusOptions{Max: 10}, quietLog); err != nil {
		t.Fatal(err)
	}
	cs, _ := ReadAll[CensusRecord](col.Store, "census")
	by := map[string]CensusRecord{}
	for _, r := range cs {
		by[r.ChannelHash] = r
	}
	if len(cs) != 3 {
		t.Fatalf("census records %+v; want the 3 new channels", cs)
	}
	n1, n2, n3 := by[shortHash(chNew1)], by[shortHash(chNew2)], by[shortHash(chNew3)]
	if !n1.Core || !n1.CountryIraq || n1.FoundBy != "both" || n1.Subscribers.Min != 25000 {
		t.Errorf("new1 %+v; want core by country, found by both engines, 25K subscribers", n1)
	}
	if !n2.Core || !n2.MetaMatch || n2.CountryIraq {
		t.Errorf("new2 %+v; want core by its own title", n2)
	}
	if n3.Core || n3.FoundBy != "web" {
		t.Errorf("new3 %+v; want mention only, found by the website", n3)
	}
	// First page (2 per page in the fake): 2 videos in 10 days, older ones
	// exist, so the rate is scaled: 6 a month.
	if !n1.UploadsKnown || n1.UploadsPerMonth != 6 || !n1.NewestComments.OK || n1.NewestComments.Min != 12 {
		t.Errorf("new1 activity %+v; want 6 uploads a month and 12 comments on the newest video", n1)
	}
	// A second census run finds nothing new to profile.
	if err := Census(context.Background(), col, CensusOptions{Max: 10}, quietLog); err == nil {
		t.Error("a second census should have nothing left to profile")
	}

	var rep strings.Builder
	if err := Report(col.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := rep.String()
	for _, want := range []string{
		"## Discovery: website search vs official API (E9)",
		"| ... not in the list | 3 | 1 |",
		"| Quota units used | n/a | 100 |",
		"## Channel census (E10)",
		"| Iraq channels (core) / mention only | 67% / 33% |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{chNew1, chNew2, chNew3, "zqbrandx", "أخبار", "البصرة", "fakeS2"} {
		if strings.Contains(out, leak) {
			t.Errorf("report leaks %q", leak)
		}
	}
}

func TestUploadsPerMonth(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	cases := []struct {
		ages []time.Duration
		more bool
		want float64
	}{
		{[]time.Duration{1 * day, 5 * day, 40 * day}, true, 2},           // two in the last 30 days
		{[]time.Duration{1 * day, 2 * day, 3 * day, 10 * day}, true, 12}, // 4 in 10 days, more pages: 12 a month
		{[]time.Duration{1 * day, 2 * day, 3 * day, 10 * day}, false, 4}, // the whole history: 4 uploads
		{nil, false, 0},
	}
	for _, c := range cases {
		got, ok := uploadsPerMonth(datedItems(now, c.ages...), c.more, now)
		if !ok || got != c.want {
			t.Errorf("ages %v: got %v (%v), want %v", c.ages, got, ok, c.want)
		}
	}
}

func datedItems(now time.Time, ages ...time.Duration) []ytparse.VideoItem {
	var out []ytparse.VideoItem
	for i, a := range ages {
		out = append(out, ytparse.VideoItem{VideoID: string(rune('a' + i)), Published: ytparse.TimeEstimate{At: now.Add(-a), OK: true}})
	}
	return out
}
