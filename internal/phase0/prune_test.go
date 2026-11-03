package phase0

import (
	"testing"
	"time"
)

// TestPruneAPIDataKeepsEverythingElse: only official-API data older than
// 30 days goes; scraped data, fresh API data and labels stay.
func TestPruneAPIDataKeepsEverythingElse(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old, fresh := now.Add(-31*24*time.Hour), now.Add(-29*24*time.Hour)
	for _, r := range []AccuracyRecord{{At: old, VideoHash: "a"}, {At: fresh, VideoHash: "b"}} {
		_ = s.Append("accuracy", r)
	}
	for _, it := range []DiscoverItem{
		{At: old, Engine: "api", VideoID: "fakeAPIonly", Title: "t1"},
		{At: old, Engine: "api", VideoID: "fakeBoth", Title: "t2"},
		{At: old, Engine: "web", VideoID: "fakeBoth", Title: "t2"},
		{At: old, Engine: "web", VideoID: "fakeWebOld", Title: "t3"},
		{At: fresh, Engine: "api", VideoID: "fakeAPIfresh", Title: "t4"},
	} {
		_ = s.Append("discover_items", it)
	}
	for i, id := range []string{"fakeAPIonly", "fakeBoth", "fakeWebOld", "fakeAPIfresh"} {
		_ = s.Append("label_sample", LabelItem{Index: i, VideoID: id, Title: "title " + id})
	}
	_ = s.Append("labels", LabelAnswer{Index: 0, Relevant: true})
	_ = s.Append("crawl_channels", ChannelRecord{At: old, ChannelHash: "c"})

	n, err := PruneAPIData(s, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("removed %d; want 1 accuracy record, 2 old API results and 1 sample title", n)
	}
	acc, _ := ReadAll[AccuracyRecord](s, "accuracy")
	if len(acc) != 1 || acc[0].VideoHash != "b" {
		t.Errorf("accuracy %+v", acc)
	}
	items, _ := ReadAll[DiscoverItem](s, "discover_items")
	if len(items) != 3 {
		t.Errorf("discover items %+v; want the two website results and the fresh API one", items)
	}
	sample, _ := ReadAll[LabelItem](s, "label_sample")
	if sample[0].Title != "" || sample[0].VideoID != "" || sample[1].Title == "" || sample[3].Title == "" {
		t.Errorf("sample %+v; want only the API-only title blanked", sample)
	}
	if labels, _ := ReadAll[LabelAnswer](s, "labels"); len(labels) != 1 {
		t.Error("labels must be kept")
	}
	if chans, _ := ReadAll[ChannelRecord](s, "crawl_channels"); len(chans) != 1 {
		t.Error("scraped data must be kept, however old")
	}
	// A second run finds nothing more to remove.
	if n, _ := PruneAPIData(s, now); n != 0 {
		t.Errorf("second run removed %d", n)
	}
}
