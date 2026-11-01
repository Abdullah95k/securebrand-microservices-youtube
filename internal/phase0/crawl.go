package phase0

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// CrawlOptions controls the crawl experiment (E3, E5, E6, E13).
type CrawlOptions struct {
	Window          time.Duration // backfill window (D12: 365 days)
	MaxTabPages     int           // safety limit per tab
	VideosPerChan   int           // videos sampled for the deep check
	CommentLimit    int           // comments read per video at most
	Workers         int           // channels processed in parallel
	WithPlayer      bool          // also call the player endpoint
	IncludeComments bool
}

// DefaultCrawlOptions returns the Phase 0 defaults.
func DefaultCrawlOptions() CrawlOptions {
	return CrawlOptions{
		Window: 365 * 24 * time.Hour, MaxTabPages: 80, VideosPerChan: 8, CommentLimit: 2000,
		Workers: 4, WithPlayer: true, IncludeComments: true,
	}
}

// ChannelRecord is one crawled channel (counts and flags only).
type ChannelRecord struct {
	At             time.Time       `json:"at"`
	ChannelHash    string          `json:"channel_hash"`
	Error          string          `json:"error,omitempty"`
	Fields         map[string]bool `json:"fields"` // which fields came back
	Subscribers    ytparse.Count   `json:"subscribers"`
	TotalVideos    ytparse.Count   `json:"total_videos"`
	VideosInWindow int             `json:"videos_in_window"`
	ShortsInWindow int             `json:"shorts_in_window"`
	LiveInWindow   int             `json:"live_in_window"`
	TabPages       int             `json:"tab_pages"`
	TabLimitHit    bool            `json:"tab_limit_hit"`
	PostsFound     int             `json:"posts_found"`
	ListCost       Cost            `json:"list_cost"` // channel + about + tabs + posts
	Elapsed        time.Duration   `json:"elapsed_ns"`
}

// VideoRecord is one video checked in depth (counts and flags only).
type VideoRecord struct {
	At          time.Time       `json:"at"`
	ChannelHash string          `json:"channel_hash"`
	VideoHash   string          `json:"video_hash"`
	Kind        ytparse.Kind    `json:"kind"`
	Error       string          `json:"error,omitempty"`
	Fields      map[string]bool `json:"fields"`
	Views       ytparse.Count   `json:"views"`
	Likes       ytparse.Count   `json:"likes"`
	PlayerErr   string          `json:"player_err,omitempty"`
	Comments    SweepResult     `json:"comments"`
	VideoCost   Cost            `json:"video_cost"`   // watch + player
	CommentCost Cost            `json:"comment_cost"` // comment sweep
}

// Crawl runs the crawl experiment over the given channel inputs.
func Crawl(ctx context.Context, col *Collector, inputs []string, o CrawlOptions, log *slog.Logger) error {
	jobs := make(chan string)
	var wg sync.WaitGroup
	workers := o.Workers
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for in := range jobs {
				crawlChannel(ctx, col, in, o, log)
			}
		}()
	}
	for _, in := range inputs {
		select {
		case jobs <- in:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}

func crawlChannel(ctx context.Context, col *Collector, input string, o CrawlOptions, log *slog.Logger) {
	start := col.Now()
	rec := ChannelRecord{At: start, Fields: map[string]bool{}}
	id, err := col.Resolve(ctx, input, &rec.ListCost)
	if err != nil {
		rec.ChannelHash, rec.Error = shortHash(input), err.Error()
		_ = col.Store.Append("crawl_channels", rec)
		log.Warn("channel not resolved", "channel", shortHash(input), "err", err)
		return
	}
	rec.ChannelHash = shortHash(id)
	ch, err := col.Channel(ctx, id, &rec.ListCost)
	if err != nil {
		rec.Error = err.Error()
		_ = col.Store.Append("crawl_channels", rec)
		log.Warn("channel page failed", "channel", rec.ChannelHash, "err", err)
		return
	}
	c, a := ch.Channel, ch.About
	rec.Fields = map[string]bool{
		"id": c.ID != "", "title": c.Title != "", "handle": c.Handle != "", "description": c.Description != "",
		"avatar": c.AvatarURL != "", "banner": c.BannerURL != "", "subscribers": c.Subscribers.OK || a.Subscribers.OK,
		"about": ch.HasAbout, "country": a.Country != "", "joined": a.JoinedText != "", "total_views": a.Views.OK,
		"links": len(a.Links) > 0, "tab_videos": c.Tabs["videos"].Params != "", "tab_shorts": c.Tabs["shorts"].Params != "",
		"tab_posts": c.Tabs["posts"].Params != "" || c.Tabs["community"].Params != "",
	}
	rec.Subscribers = a.Subscribers
	if !rec.Subscribers.OK {
		rec.Subscribers = c.Subscribers
	}
	rec.TotalVideos = a.Videos
	if !rec.TotalVideos.OK {
		rec.TotalVideos = c.VideoCount
	}

	since := col.Now().Add(-o.Window)
	var all []ytparse.VideoItem
	for _, key := range []string{"videos", "shorts", "streams"} {
		t, ok := c.Tabs[key]
		if !ok {
			continue
		}
		tr, err := col.Tab(ctx, id, t.Params, since, o.MaxTabPages, &rec.ListCost)
		rec.TabPages += tr.Pages
		rec.TabLimitHit = rec.TabLimitHit || tr.ReachedLimit
		if err != nil {
			log.Warn("tab failed", "channel", rec.ChannelHash, "tab", key, "err", err)
		}
		for _, v := range tr.Items {
			switch {
			case key == "shorts" || v.Kind == ytparse.KindShort:
				v.Kind = ytparse.KindShort
				rec.ShortsInWindow++
			case key == "streams" || v.Kind == ytparse.KindLive:
				rec.LiveInWindow++
			default:
				rec.VideosInWindow++
			}
			all = append(all, v)
		}
	}
	if t, ok := c.Tabs["posts"]; ok {
		rec.PostsFound = countPosts(ctx, col, id, t.Params, &rec.ListCost)
	} else if t, ok := c.Tabs["community"]; ok {
		rec.PostsFound = countPosts(ctx, col, id, t.Params, &rec.ListCost)
	}
	rec.Elapsed = col.Now().Sub(start)
	_ = col.Store.Append("crawl_channels", rec)
	log.Info("channel crawled", "channel", rec.ChannelHash, "videos", rec.VideosInWindow, "shorts", rec.ShortsInWindow,
		"live", rec.LiveInWindow, "requests", rec.ListCost.Requests, "blocks", rec.ListCost.Blocks)

	for _, v := range sampleVideos(all, o.VideosPerChan) {
		if ctx.Err() != nil {
			return
		}
		crawlVideo(ctx, col, rec.ChannelHash, v, o, log)
	}
}

func countPosts(ctx context.Context, col *Collector, id, params string, cost *Cost) int {
	r, err := col.call(ctx, cost, "browse", "browse:posts", map[string]any{"browseId": id, "params": params})
	if err != nil {
		return 0
	}
	posts, _ := ytparse.ParsePosts(r.Data, col.Now())
	return len(posts)
}

// sampleVideos picks a spread: newest, middle and oldest videos in the
// window, plus at least one Short and one live stream when present.
func sampleVideos(all []ytparse.VideoItem, n int) []ytparse.VideoItem {
	if n <= 0 || len(all) == 0 {
		return nil
	}
	var regular, shorts, live []ytparse.VideoItem
	for _, v := range all {
		switch v.Kind {
		case ytparse.KindShort:
			shorts = append(shorts, v)
		case ytparse.KindLive, ytparse.KindUpcoming:
			live = append(live, v)
		default:
			regular = append(regular, v)
		}
	}
	var out []ytparse.VideoItem
	seen := map[string]bool{}
	take := func(v ytparse.VideoItem) {
		if len(out) < n && !seen[v.VideoID] {
			seen[v.VideoID] = true
			out = append(out, v)
		}
	}
	if len(shorts) > 0 {
		take(shorts[0])
	}
	if len(live) > 0 {
		take(live[0])
	}
	// Regular videos are listed newest first; take evenly spaced ones.
	if k := n - len(out); k > 0 && len(regular) > 0 {
		if k >= len(regular) {
			for _, v := range regular {
				take(v)
			}
		} else {
			for i := 0; i < k; i++ {
				take(regular[i*(len(regular)-1)/max(1, k-1)])
			}
		}
	}
	return out
}

func crawlVideo(ctx context.Context, col *Collector, chHash string, item ytparse.VideoItem, o CrawlOptions, log *slog.Logger) {
	rec := VideoRecord{At: col.Now(), ChannelHash: chHash, VideoHash: shortHash(item.VideoID), Kind: item.Kind, Fields: map[string]bool{}}
	vr, err := col.Video(ctx, item.VideoID, o.WithPlayer, &rec.VideoCost)
	if err != nil {
		rec.Error = err.Error()
		_ = col.Store.Append("crawl_videos", rec)
		return
	}
	w, p := vr.Watch, vr.Player
	rec.Views, rec.Likes, rec.PlayerErr = w.Views, w.Likes, vr.PlayerErr
	rec.Fields = map[string]bool{
		"title": w.Title != "", "description": w.Description != "" || p.Description != "", "channel_id": w.ChannelID != "",
		"views_exact": w.Views.Exact || p.Views > 0, "likes": w.Likes.OK, "likes_exact": w.Likes.Exact,
		"date_text": w.DateText != "", "chapters": len(w.Chapters) > 0, "comments_token": w.CommentsToken != "",
		"comment_count": w.CommentCount.OK,
		"player_ok":     p.VideoID != "", "tags": len(p.Keywords) > 0, "category": p.Category != "",
		"length": p.LengthSec > 0 || item.DurationSec > 0, "publish_datetime": strings.Contains(p.PublishDate, "T"),
		"publish_date_only": p.PublishDate != "" && !strings.Contains(p.PublishDate, "T"), "captions_listed": len(p.CaptionLangs) > 0,
	}
	if o.IncludeComments {
		sw, err := col.Sweep(ctx, item.VideoID, w.ChannelID, w.CommentsToken, o.CommentLimit, &rec.CommentCost)
		if err != nil {
			rec.Error = "comments: " + err.Error()
		}
		if !sw.Displayed.OK {
			sw.Displayed = w.CommentCount
		}
		rec.Comments = sw
		if len(sw.Snapshot) > 0 {
			_ = col.Store.WriteJSON("snapshots/"+rec.VideoHash+".json", Snapshot{
				VideoID: item.VideoID, Taken: col.Now(), CommentsToken: w.CommentsToken, Comments: sortedSnap(sw.Snapshot),
			})
		}
	}
	_ = col.Store.Append("crawl_videos", rec)
	log.Info("video checked", "video", rec.VideoHash, "kind", rec.Kind, "comments", rec.Comments.TopLevel+rec.Comments.Replies,
		"displayed", rec.Comments.Displayed.Min, "requests", rec.VideoCost.Requests+rec.CommentCost.Requests)
}

// Snapshot is the comment state of one video for the 7-day re-sweep (E7).
// It keeps comment IDs and text hashes only.
type Snapshot struct {
	VideoID       string        `json:"video_id"`
	Taken         time.Time     `json:"taken"`
	CommentsToken string        `json:"comments_token"`
	Comments      []SnapComment `json:"comments"`
}

func sortedSnap(in []SnapComment) []SnapComment {
	out := append([]SnapComment(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
