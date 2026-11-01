package phase0

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// AccuracyRecord compares scraped numbers with the official API for one
// video (E4, approved one-time check, D13).
type AccuracyRecord struct {
	At               time.Time `json:"at"`
	VideoHash        string    `json:"video_hash"`
	Found            bool      `json:"found"`
	ViewsScraped     int64     `json:"views_scraped"`
	ViewsOfficial    int64     `json:"views_official"`
	ViewsRelDiff     float64   `json:"views_rel_diff"`
	LikesScraped     int64     `json:"likes_scraped"`
	LikesExact       bool      `json:"likes_exact"`
	LikesOfficial    int64     `json:"likes_official"`
	LikesRelDiff     float64   `json:"likes_rel_diff"`
	LikesInRange     bool      `json:"likes_in_range"` // official value inside the scraped range
	CommentsCount    int64     `json:"comments_scraped_displayed"`
	CommentsOfficial int64     `json:"comments_official"`
}

// Accuracy fetches the snapshotted videos (up to max) from the official API
// in batches of 50 and compares them with fresh scraped numbers. It costs one
// quota unit per 50 videos.
func Accuracy(ctx context.Context, col *Collector, api *ytapi.Client, max int, log *slog.Logger) error {
	names, err := col.Store.List("snapshots")
	if err != nil {
		return err
	}
	type pair struct {
		id    string
		watch ytparse.WatchInfo
	}
	var batch []pair
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		ids := make([]string, len(batch))
		for i, p := range batch {
			ids[i] = p.id
		}
		official, err := api.VideosList(ctx, ids)
		if err != nil {
			return err
		}
		byID := map[string]ytapi.VideoStats{}
		for _, o := range official {
			byID[o.VideoID] = o
		}
		for _, p := range batch {
			o, ok := byID[p.id]
			rec := AccuracyRecord{At: col.Now(), VideoHash: shortHash(p.id), Found: ok,
				ViewsScraped: p.watch.Views.Min, LikesScraped: p.watch.Likes.Min, LikesExact: p.watch.Likes.Exact,
				CommentsCount: p.watch.CommentCount.Min}
			if ok {
				rec.ViewsOfficial, rec.LikesOfficial, rec.CommentsOfficial = o.Views, o.Likes, o.Comments
				rec.ViewsRelDiff = relDiff(rec.ViewsScraped, o.Views)
				rec.LikesRelDiff = relDiff(rec.LikesScraped, o.Likes)
				rec.LikesInRange = o.Likes >= p.watch.Likes.Min && o.Likes < p.watch.Likes.Max
			}
			_ = col.Store.Append("accuracy", rec)
		}
		log.Info("accuracy batch compared", "videos", len(batch))
		batch = batch[:0]
		return nil
	}
	for i, name := range names {
		if i >= max || ctx.Err() != nil {
			break
		}
		var snap Snapshot
		if ok, err := col.Store.ReadJSON("snapshots/"+name, &snap); !ok || err != nil {
			continue
		}
		var cost Cost
		vr, err := col.Video(ctx, snap.VideoID, false, &cost)
		if err != nil {
			continue
		}
		batch = append(batch, pair{id: snap.VideoID, watch: vr.Watch})
		if len(batch) == 50 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func relDiff(scraped, official int64) float64 {
	if official == 0 {
		if scraped == 0 {
			return 0
		}
		return 1
	}
	d := float64(scraped-official) / float64(official)
	if d < 0 {
		d = -d
	}
	return d
}
