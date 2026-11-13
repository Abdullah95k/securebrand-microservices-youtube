package writer_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fill"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

const ch = "UCfakeWRITER000000000001"

type rig struct {
	pool *pgxpool.Pool
	w    *writer.Writer
	sup  *privacy.Suppressor
	now  time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := kwstore.ImportSeed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var live kwstore.Live
	if _, err := live.Refresh(ctx, pool); err != nil {
		t.Fatal(err)
	}
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	r := &rig{pool: pool, sup: privacy.NewSuppressor(k), now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	r.w = &writer.Writer{Pool: pool, Sup: r.sup, KW: &live, Now: func() time.Time { return r.now }}
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: "@fakewriter", Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	return r
}

// events lists the kinds of the outbox events about id, oldest first.
func (r *rig) events(t *testing.T, id string) []string {
	t.Helper()
	rows, err := r.pool.Query(context.Background(), `SELECT kind FROM event_outbox WHERE entity_id = $1 ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

func sameEvents(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("%s: outbox events %v, want %v", what, got, want)
	}
}

func (r *rig) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChannelChangesAndSnapshots(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := writer.ChannelFrom(ytparse.Channel{ID: ch, Title: "Old title", Handle: "@fakewriter", Subscribers: ytparse.ParseCount("1.2K subscribers")},
		ytparse.About{Country: "Iraq", Links: []ytparse.Link{{Title: "site", URL: "example.org"}}, Views: ytparse.ParseCount("1,000 views")}, true)
	changed, err := r.w.Channel(ctx, c)
	if err != nil || len(changed) != 0 {
		t.Fatalf("first fill: changed %v, err %v", changed, err)
	}
	r.now = r.now.Add(time.Hour)
	c.Title = "New title"
	changed, err = r.w.Channel(ctx, c)
	if err != nil || len(changed) != 1 || changed[0] != "title" {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	if n := r.count(t, `SELECT count(*) FROM channel_changes WHERE field = 'title' AND old_value = 'Old title'`); n != 1 {
		t.Error("the title change must be logged")
	}
	if n := r.count(t, `SELECT count(*) FROM channel_snapshots WHERE channel_id = $1 AND subscribers_min = 1200`, ch); n != 2 {
		t.Errorf("want a snapshot per check, got %d", n)
	}
	// Two misses mark it removed; the row stays.
	if removed, _ := r.w.ChannelMissing(ctx, ch); removed {
		t.Error("one miss is not enough")
	}
	if removed, _ := r.w.ChannelMissing(ctx, ch); !removed {
		t.Error("the second miss marks it removed")
	}
	if n := r.count(t, `SELECT count(*) FROM channels WHERE id = $1 AND status = 'removed' AND removed_at IS NOT NULL AND title = 'New title'`, ch); n != 1 {
		t.Error("a removed channel keeps its data")
	}
	// Events: the first read (content and counts), the new title (the
	// counts did not change), nothing for the first miss, the removal.
	sameEvents(t, "channel", r.events(t, ch), "channel", "channel_snapshot", "channel", "channel")
}

// A live stream's player has no length: its readings don't count towards
// the length's fill rate, so the evening's live streams don't look like a
// page change (FillRateDrop, 13 November). Its other player fields count.
func TestLiveStreamsLeaveTheLengthFillRateAlone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.w.Fill = &fill.Recorder{}
	for _, v := range []writer.Video{
		{ID: "fakeVIDlen1", ChannelID: ch, HasPlayer: true, Watch: ytparse.WatchInfo{Title: "t"},
			Player: ytparse.PlayerInfo{PublishDate: "2026-11-13T08:00:00Z", Category: "News", LengthSec: 90}},
		{ID: "fakeVIDlive", ChannelID: ch, HasPlayer: true, Watch: ytparse.WatchInfo{Title: "t"},
			Player: ytparse.PlayerInfo{PublishDate: "2026-11-13T08:00:00Z", Category: "News", IsLive: true, WasLive: true}},
	} {
		if _, err := r.w.Video(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.w.Fill.Flush(ctx, r.pool, r.now); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string][2]int{"length": {1, 1}, "category": {2, 2}} {
		var seen, present int
		if err := r.pool.QueryRow(ctx, `SELECT seen, present FROM field_fill_rates WHERE endpoint = 'player' AND field = $1`, field).
			Scan(&seen, &present); err != nil {
			t.Fatal(err)
		}
		if seen != want[0] || present != want[1] {
			t.Errorf("player.%s: seen %d, present %d; want %v", field, seen, present, want)
		}
	}
}

func TestVideosRelevancePrecisionAndSuppression(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	item := ytparse.VideoItem{VideoID: "fakeVIDw01", Title: "أخبار بغداد", Kind: ytparse.KindVideo, DurationSec: 90,
		Published: ytparse.TimeEstimate{At: r.now.Add(-3 * 24 * time.Hour), Precision: 12 * time.Hour, OK: true}, PublishedText: "3 days ago"}
	if isNew, err := r.w.Listed(ctx, ch, item); err != nil || !isNew {
		t.Fatalf("listed: new %v, err %v", isNew, err)
	}
	if isNew, _ := r.w.Listed(ctx, ch, item); isNew {
		t.Error("a known video is not new")
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = 'fakeVIDw01' AND relevant AND published_precision_s = 43200`); n != 1 {
		t.Error("the listing must store the relevance and the date estimate")
	}
	// A video first seen through the official API is stored, but its
	// events wait for its website read (the API's title never goes out).
	if isNew, err := r.w.ListedVia(ctx, ch, ytparse.VideoItem{VideoID: "fakeVIDapi", Title: "an API title"}, "api_search"); err != nil || !isNew {
		t.Fatalf("API listing: new %v, err %v", isNew, err)
	}
	sameEvents(t, "API-sourced listing", r.events(t, "fakeVIDapi"))
	// The video check: exact publish date from the player wins; numbers snapshot.
	v := writer.Video{ID: "fakeVIDw01", ChannelID: ch, HasPlayer: true,
		Watch:  ytparse.WatchInfo{Title: "أخبار بغداد", Description: "d", Likes: ytparse.ParseCount("1.5K"), CommentCount: ytparse.ParseCount("12"), CommentsToken: "tok"},
		Player: ytparse.PlayerInfo{Views: 4321, PublishDate: "2026-10-31T08:00:00-07:00", Keywords: []string{"a"}, Category: "News", LengthSec: 90, FamilySafe: true}}
	out, err := r.w.Video(ctx, v)
	if err != nil || !out.Relevant || out.Published.IsZero() || out.CommentsOff {
		t.Fatalf("video: %+v, err %v", out, err)
	}
	var prec int
	var views int64
	_ = r.pool.QueryRow(ctx, `SELECT published_precision_s, views FROM videos WHERE id = 'fakeVIDw01'`).Scan(&prec, &views)
	if prec != 0 || views != 4321 {
		t.Errorf("precision %d, views %d; want the exact player values", prec, views)
	}
	// A later check that only has an estimate keeps the exact date; a title edit is logged.
	r.now = r.now.Add(6 * time.Hour)
	v.Player, v.HasPlayer = ytparse.PlayerInfo{}, false
	v.Watch.Title = "أخبار بغداد (محدث)"
	v.PublishedEst = ytparse.TimeEstimate{At: r.now, Precision: time.Hour, OK: true}
	out, err = r.w.Video(ctx, v)
	if err != nil || len(out.Changed) != 1 || out.Changed[0] != "title" {
		t.Fatalf("second check: %+v, err %v", out, err)
	}
	_ = r.pool.QueryRow(ctx, `SELECT published_precision_s FROM videos WHERE id = 'fakeVIDw01'`).Scan(&prec)
	if prec != 0 {
		t.Error("an estimate must not replace an exact date")
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = 'fakeVIDw01' AND tags = '{a}' AND category = 'News'`); n != 1 {
		t.Error("tags and category stay when the player was not read")
	}
	if n := r.count(t, `SELECT count(*) FROM video_snapshots WHERE video_id = 'fakeVIDw01'`); n != 2 {
		t.Errorf("want 2 snapshots, got %d", n)
	}
	// Erased videos are never written.
	if err := r.sup.Add(ctx, r.pool, privacy.Video, "fakeVIDw02", "erasure request", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.Listed(ctx, ch, ytparse.VideoItem{VideoID: "fakeVIDw02", Title: "x"}); !errors.Is(err, writer.ErrSuppressed) {
		t.Errorf("err %v; want ErrSuppressed", err)
	}
	if _, err := r.w.Video(ctx, writer.Video{ID: "fakeVIDw02", ChannelID: ch}); !errors.Is(err, writer.ErrSuppressed) {
		t.Errorf("err %v; want ErrSuppressed", err)
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = 'fakeVIDw02'`); n != 0 {
		t.Error("an erased video was stored")
	}
	// Missing twice → removed, content kept.
	_, _ = r.w.VideoMissing(ctx, "fakeVIDw01")
	if removed, _ := r.w.VideoMissing(ctx, "fakeVIDw01"); !removed {
		t.Error("the second miss marks the video removed")
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = 'fakeVIDw01' AND removed_at IS NOT NULL AND title IS NOT NULL`); n != 1 {
		t.Error("a removed video keeps its data")
	}
	// Events: the new listing (at once, before its check), not the known
	// one; the first check (details and views); the edited title (same
	// counts); the removal. Nothing for the erased video.
	sameEvents(t, "video", r.events(t, "fakeVIDw01"), "video", "video_metrics", "video", "video_metrics", "video", "video")
	sameEvents(t, "erased video", r.events(t, "fakeVIDw02"))
}

func TestPostsAndPlaylistsAddEvents(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	p := ytparse.Post{ID: "UgkxFakePost01", Text: "A post", Likes: ytparse.ParseCount("12"), Comments: ytparse.ParseCount("3")}
	if _, err := r.w.Post(ctx, ch, p); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour)
	if _, err := r.w.Post(ctx, ch, p); err != nil { // read again, unchanged
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour)
	p.Likes = ytparse.ParseCount("15")
	if _, err := r.w.Post(ctx, ch, p); err != nil {
		t.Fatal(err)
	}
	sameEvents(t, "post", r.events(t, p.ID), "post", "post_metrics", "post_metrics")

	pl := ytparse.Playlist{ID: "PLfakeWRITER01", Title: "Episodes", VideoCount: ytparse.ParseCount("2 videos")}
	if _, err := r.w.Playlist(ctx, ch, pl); err != nil {
		t.Fatal(err)
	}
	items := []ytparse.PlaylistEntry{{VideoID: "fakeVIDw01"}, {VideoID: "fakeVIDw03"}}
	if err := r.w.PlaylistItems(ctx, pl.ID, items, true); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour)
	if err := r.w.PlaylistItems(ctx, pl.ID, items, true); err != nil { // the same list
		t.Fatal(err)
	}
	if err := r.w.PlaylistItems(ctx, pl.ID, items[:1], true); err != nil { // one removed
		t.Fatal(err)
	}
	sameEvents(t, "playlist", r.events(t, pl.ID), "playlist", "playlist", "playlist")
}
