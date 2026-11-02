package phase0

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/feeds"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
)

// DetectOptions controls the new-upload detection experiment (E8). WebSub is
// not part of Phase 0 until the push subdomain exists (D23).
type DetectOptions struct {
	RSSEvery time.Duration // feed check interval per channel (default 15 min)
	TabEvery time.Duration // channel tab check interval (default 24 h)
	Tick     time.Duration // how often the loop looks for due checks (default 5 s; tests use less)
}

// FeedRecord is one feed fetch (E8: errors, timing; plan §3.4 404 bursts).
type FeedRecord struct {
	At          time.Time `json:"at"`
	ChannelHash string    `json:"channel_hash"`
	Outcome     string    `json:"outcome"` // ok, not_modified, not_found, error
	Error       string    `json:"error,omitempty"`
	Entries     int       `json:"entries"`
	WireBytes   int64     `json:"wire_bytes"`
	ElapsedMS   int64     `json:"elapsed_ms"`
}

// SightingRecord marks the first time a layer saw a new upload.
// tabNewWithin is how recently a video found by a tab check must have been
// published to count as a new upload.
const tabNewWithin = 48 * time.Hour

type SightingRecord struct {
	At          time.Time `json:"at"`
	ChannelHash string    `json:"channel_hash"`
	VideoHash   string    `json:"video_hash"`
	Layer       string    `json:"layer"`     // rss or tab
	Published   time.Time `json:"published"` // feed time (rss) or the tab's relative time (tab, approximate)
	DelaySec    float64   `json:"delay_sec"` // first seen − published (rss only)
	// RSS stats freshness: views reported by the feed at first sighting.
	FeedViews int64 `json:"feed_views"`
}

type detectState struct {
	// Seen maps "layer:videoID" → true. It persists across restarts.
	Seen map[string]bool `json:"seen"`
	// Primed is true once every channel's current uploads were recorded, so
	// later sightings are genuinely new.
	Primed map[string]bool `json:"primed"`
}

// Detect runs until ctx is cancelled, checking feeds and channel tabs on
// their intervals and recording first sightings.
func Detect(ctx context.Context, col *Collector, fetcher *feeds.Fetcher, channelIDs []string, o DetectOptions, log *slog.Logger) error {
	if o.RSSEvery <= 0 {
		o.RSSEvery = 15 * time.Minute
	}
	if o.TabEvery <= 0 {
		o.TabEvery = 24 * time.Hour
	}
	st := detectState{Seen: map[string]bool{}, Primed: map[string]bool{}}
	if _, err := col.Store.ReadJSON("detect_state.json", &st); err != nil {
		return err
	}
	if st.Seen == nil {
		st.Seen = map[string]bool{}
	}
	if st.Primed == nil {
		st.Primed = map[string]bool{}
	}

	nextRSS := map[string]time.Time{}
	nextTab := map[string]time.Time{}
	// Spread first checks evenly over one interval instead of a burst.
	for i, id := range channelIDs {
		offset := time.Duration(i) * o.RSSEvery / time.Duration(max(1, len(channelIDs)))
		nextRSS[id] = col.Now().Add(offset)
		nextTab[id] = col.Now().Add(offset)
	}
	if o.Tick <= 0 {
		o.Tick = 5 * time.Second
	}
	tick := time.NewTicker(o.Tick)
	defer tick.Stop()
	for {
		now := col.Now()
		for _, id := range channelIDs {
			if ctx.Err() != nil {
				return saveState(col, st, ctx.Err())
			}
			if !now.Before(nextRSS[id]) {
				nextRSS[id] = now.Add(o.RSSEvery)
				checkFeed(ctx, col, fetcher, id, &st, log)
			}
			if !now.Before(nextTab[id]) {
				nextTab[id] = now.Add(o.TabEvery)
				checkTab(ctx, col, id, &st, log)
				st.Primed[id] = true
			}
		}
		if err := saveState(col, st, nil); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return saveState(col, st, ctx.Err())
		case <-tick.C:
		}
	}
}

func saveState(col *Collector, st detectState, err error) error {
	if serr := col.Store.WriteJSON("detect_state.json", st); serr != nil {
		return serr
	}
	return err
}

func checkFeed(ctx context.Context, col *Collector, fetcher *feeds.Fetcher, id string, st *detectState, log *slog.Logger) {
	rec := FeedRecord{At: col.Now(), ChannelHash: shortHash(id)}
	res, err := fetcher.Fetch(ctx, id)
	if res != nil {
		rec.WireBytes, rec.ElapsedMS = res.WireBytes, res.Elapsed.Milliseconds()
	}
	switch {
	case errors.Is(err, feeds.ErrNotModified):
		rec.Outcome = "not_modified"
	case errors.Is(err, innertube.ErrNotFound):
		rec.Outcome = "not_found"
	case err != nil:
		rec.Outcome, rec.Error = "error", err.Error()
	default:
		rec.Outcome, rec.Entries = "ok", len(res.Feed.Entries)
		for _, e := range res.Feed.Entries {
			key := "rss:" + e.VideoID
			if st.Seen[key] {
				continue
			}
			st.Seen[key] = true
			if !st.Primed[id] {
				continue // existing uploads at start-up are not "new"
			}
			s := SightingRecord{At: rec.At, ChannelHash: rec.ChannelHash, VideoHash: shortHash(e.VideoID), Layer: "rss", Published: e.Published, FeedViews: e.Views}
			if !e.Published.IsZero() {
				s.DelaySec = rec.At.Sub(e.Published).Seconds()
			}
			_ = col.Store.Append("detect_sightings", s)
			log.Info("new upload seen", "layer", "rss", "channel", rec.ChannelHash, "delay_min", int(s.DelaySec/60))
		}
	}
	_ = col.Store.Append("detect_feeds", rec)
}

func checkTab(ctx context.Context, col *Collector, id string, st *detectState, log *slog.Logger) {
	var cost Cost
	ch, err := col.Channel(ctx, id, &cost)
	if err != nil {
		log.Warn("tab check failed", "channel", shortHash(id), "err", err)
		return
	}
	for _, key := range []string{"videos", "shorts", "streams"} {
		t, ok := ch.Channel.Tabs[key]
		if !ok {
			continue
		}
		tr, err := col.Tab(ctx, id, t.Params, col.Now().Add(-7*24*time.Hour), 2, &cost)
		if err != nil {
			continue
		}
		for _, v := range tr.Items {
			k := "tab:" + v.VideoID
			if st.Seen[k] {
				continue
			}
			st.Seen[k] = true
			// Only a recent upload is a new sighting. A tab check can reach
			// videos it never listed before that are not new at all: once
			// tabs read their second page (2 November 2026), thousands of
			// older videos were counted as "first seen by tab check".
			// Shorts carry no date on the tab, so they are left to RSS.
			now := col.Now()
			if st.Primed[id] && v.Published.OK && now.Sub(v.Published.At) <= tabNewWithin {
				_ = col.Store.Append("detect_sightings", SightingRecord{At: now, ChannelHash: shortHash(id), VideoHash: shortHash(v.VideoID), Layer: "tab", Published: v.Published.At})
			}
		}
	}
}
