package phase0

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// ResweepRecord compares a video's comments now with the crawl snapshot (E7).
type ResweepRecord struct {
	At            time.Time `json:"at"`
	VideoHash     string    `json:"video_hash"`
	Error         string    `json:"error,omitempty"`
	DaysSince     float64   `json:"days_since"`
	Before        int       `json:"before"`
	After         int       `json:"after"`
	New           int       `json:"new"`
	Missing       int       `json:"missing"` // gone since the snapshot (deleted or hidden)
	Edited        int       `json:"edited"`  // same ID, different text
	LikesChanged  int       `json:"likes_changed"`
	PinChanged    int       `json:"pin_changed"`
	HeartChanged  int       `json:"heart_changed"`
	CappedBefore  bool      `json:"capped_before"`
	CappedNow     bool      `json:"capped_now"`
	StopEarlyNew  int       `json:"stop_early_new"`   // new comments found by the stop-early scan
	StopEarlyReqs int       `json:"stop_early_pages"` // pages the stop-early scan needed
	Cost          Cost      `json:"cost"`
}

// Resweep re-reads the comments of every snapshotted video and records
// edits, deletions and new comments. It also measures how many pages a
// "newest first, stop at known comments" scan needs (plan §8.7 step 5).
func Resweep(ctx context.Context, col *Collector, limit int, log *slog.Logger) error {
	names, err := col.Store.List("snapshots")
	if err != nil {
		return err
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var snap Snapshot
		if ok, err := col.Store.ReadJSON("snapshots/"+name, &snap); !ok || err != nil {
			continue
		}
		rec := ResweepRecord{At: col.Now(), VideoHash: shortHash(snap.VideoID), DaysSince: col.Now().Sub(snap.Taken).Hours() / 24, Before: len(snap.Comments)}
		rec.CappedBefore = len(snap.Comments) >= limit

		known := map[string]SnapComment{}
		for _, c := range snap.Comments {
			known[c.ID] = c
		}
		// Stop-early scan: newest first until a page has nothing new.
		vr, err := col.Video(ctx, snap.VideoID, false, &rec.Cost)
		if err != nil {
			rec.Error = err.Error()
			_ = col.Store.Append("resweep", rec)
			continue
		}
		rec.StopEarlyNew, rec.StopEarlyReqs = stopEarlyScan(ctx, col, vr.Watch.CommentsToken, known, &rec.Cost)

		sw, err := col.Sweep(ctx, snap.VideoID, vr.Watch.ChannelID, vr.Watch.CommentsToken, limit, &rec.Cost)
		if err != nil {
			rec.Error = err.Error()
		}
		rec.After, rec.CappedNow = len(sw.Snapshot), sw.Capped
		now := map[string]SnapComment{}
		for _, c := range sw.Snapshot {
			now[c.ID] = c
			old, existed := known[c.ID]
			if !existed {
				rec.New++
				continue
			}
			if old.TextHash != c.TextHash {
				rec.Edited++
			}
			if old.Likes != c.Likes {
				rec.LikesChanged++
			}
			if old.Pinned != c.Pinned {
				rec.PinChanged++
			}
			if old.Hearted != c.Hearted {
				rec.HeartChanged++
			}
		}
		// Only count a comment as missing when neither sweep was capped,
		// otherwise it may simply be beyond the cap.
		if !rec.CappedBefore && !rec.CappedNow {
			for id := range known {
				if _, ok := now[id]; !ok {
					rec.Missing++
				}
			}
		}
		_ = col.Store.Append("resweep", rec)
		log.Info("video re-swept", "video", rec.VideoHash, "new", rec.New, "missing", rec.Missing, "edited", rec.Edited,
			"stop_early_pages", rec.StopEarlyReqs)
	}
	return nil
}

func stopEarlyScan(ctx context.Context, col *Collector, commentsToken string, known map[string]SnapComment, cost *Cost) (newFound, pages int) {
	if commentsToken == "" {
		return 0, 0
	}
	r, err := col.call(ctx, cost, "next", "next:comments", map[string]any{"continuation": commentsToken})
	if err != nil {
		return 0, 0
	}
	token := ytparse.ParseComments(r.Data, col.Now()).NewestFirstToken
	for token != "" && pages < 50 {
		r, err := col.call(ctx, cost, "next", "next:comments", map[string]any{"continuation": token})
		if err != nil {
			return newFound, pages
		}
		pages++
		p := ytparse.ParseComments(r.Data, col.Now())
		fresh := 0
		for _, c := range p.Comments {
			if c.IsReply {
				continue
			}
			if _, ok := known[c.ID]; !ok {
				fresh++
			}
		}
		newFound += fresh
		if fresh == 0 {
			return newFound, pages
		}
		token = p.NextToken
	}
	return newFound, pages
}
