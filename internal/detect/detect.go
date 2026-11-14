// Package detect finds new uploads (plan §8.5): each listed channel's feed
// is read at its tier's interval, and WebSub push notifications (package
// websub) say "check now". Tab checks (package collect) catch what both
// miss. A new upload of a core channel is queued for a full read at P0; a
// mention channel's upload only when its title or description matches the
// keywords (the others are noted, not collected).
package detect

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/feeds"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// RSS reads channel feeds.
type RSS struct {
	Pool    *pgxpool.Pool
	Feeds   *feeds.Fetcher
	Write   *writer.Writer
	Log     *slog.Logger
	Now     func() time.Time
	Breaker *Breaker // nil: a default one
	// Recent reads a channel's newest uploads from its tabs
	// (collect.Service.RecentUploads). It replaces the feed while the feed
	// service answers "not found"; nil: wait for the feed to come back.
	Recent func(ctx context.Context, channelID string) (int, error)
	// MentionShare gets a mention channel's latest feed uploads: how many
	// there were and how many relate to Iraq (title or description match the
	// keywords). The worker passes discovery.MentionToCore (D-076); nil:
	// nothing.
	MentionShare func(ctx context.Context, channelID string, matching, total int) error
	once         sync.Once
}

func (r *RSS) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Handlers maps the feed job kind to its handler.
func (r *RSS) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{queue.KindRSS: r.feed}
}

func (r *RSS) breaker() *Breaker {
	r.once.Do(func() {
		if r.Breaker == nil {
			r.Breaker = &Breaker{}
		}
	})
	return r.Breaker
}

func (r *RSS) feed(ctx context.Context, j queue.Job) (time.Time, error) {
	var kind, tier, status string
	err := r.Pool.QueryRow(ctx, `SELECT kind, tier, status FROM channels WHERE id = $1`, j.EntityID).Scan(&kind, &tier, &status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "listed" {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	t := policy.Tier(tier)
	next := r.now().Add(policy.RSS(t)) // detection is never stretched
	br := r.breaker()
	if br.Open(r.now()) {
		// Feeds failing widely: read the channel's tabs instead.
		return r.fallback(ctx, j.EntityID, next, r.now().Add(30*time.Minute))
	}
	res, err := r.Feeds.Fetch(ctx, j.EntityID)
	switch {
	case errors.Is(err, feeds.ErrNotModified):
		br.Record(r.now(), true)
		return next, nil
	case errors.Is(err, innertube.ErrNotFound):
		// The feed service answers 404 for valid channels for hours at a
		// time (plan §3.4; Phase 0: all channels at once, ~9 hours at a
		// time, ~40% of the time). The tabs show the same new uploads.
		if br.Record(r.now(), false) && r.Log != nil {
			r.Log.Warn("feeds answer \"not found\" widely: new uploads are read from channel tabs for 30 minutes")
		}
		return r.fallback(ctx, j.EntityID, next, next)
	case err != nil:
		if br.Record(r.now(), false) && r.Log != nil {
			r.Log.Warn("more than 20% of feeds failing: new uploads are read from channel tabs for 30 minutes")
		}
		return time.Time{}, err
	}
	br.Record(r.now(), true)
	newCount, total, matching := 0, 0, 0
	for _, e := range res.Feed.Entries {
		if e.VideoID == "" {
			continue
		}
		isNew, err := r.Entry(ctx, j.EntityID, kind, e, "rss")
		if err != nil {
			return time.Time{}, err
		}
		if isNew {
			newCount++
		}
		if kind == registry.Mention {
			total++
			if ok, _ := r.Write.Match(e.Title, e.Description); ok {
				matching++
			}
		}
	}
	// A mention channel's latest uploads: once enough relate to Iraq, a
	// channel discovery admitted is collected in full (D-076).
	if kind == registry.Mention && r.MentionShare != nil {
		if err := r.MentionShare(ctx, j.EntityID, matching, total); err != nil {
			return time.Time{}, err
		}
	}
	if newCount > 0 && r.Log != nil {
		r.Log.Info("new uploads in feed", "channel", j.EntityID, "new", newCount)
	}
	return next, nil
}

// fallback reads the channel's tabs when Recent is set; without it, the
// job waits until "without".
func (r *RSS) fallback(ctx context.Context, channelID string, next, without time.Time) (time.Time, error) {
	if r.Recent == nil {
		return without, nil
	}
	n, err := r.Recent(ctx, channelID)
	if err != nil {
		return time.Time{}, err
	}
	if n > 0 && r.Log != nil {
		r.Log.Info("new uploads found on channel tabs (feed unavailable)", "channel", channelID, "new", n)
	}
	return next, nil
}

// Entry handles one feed or push entry: a new video is stored and, for a
// core channel or a relevant mention-channel video, queued at P0. Exact
// numbers the feed carries for known videos are kept as a snapshot. It is
// shared with the WebSub receiver.
func (r *RSS) Entry(ctx context.Context, channelID, channelKind string, e feeds.Entry, source string) (bool, error) {
	item := ytparse.VideoItem{VideoID: e.VideoID, Title: e.Title, Kind: ytparse.KindVideo}
	if !e.Published.IsZero() {
		item.Published = ytparse.TimeEstimate{At: e.Published, OK: true} // exact
	}
	isNew, err := r.Write.ListedVia(ctx, channelID, item, source)
	if errors.Is(err, writer.ErrSuppressed) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !isNew {
		if e.HasStats {
			_, err := r.Pool.Exec(ctx, `INSERT INTO video_snapshots (video_id, taken_at, views, likes_min)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, e.VideoID, r.now(), e.Views, e.Likes)
			return false, err
		}
		return false, nil
	}
	if channelKind == registry.Mention {
		if ok, _ := r.Write.Match(e.Title, e.Description); !ok {
			return true, nil // noted, not collected
		}
	}
	return true, queue.Now(ctx, r.Pool, queue.KindVideo, e.VideoID, channelID, queue.P0)
}

// Breaker opens when more than 20% of the recent feed reads failed (plan
// §8.8), and closes after 30 minutes.
type Breaker struct {
	mu        sync.Mutex
	window    []bool
	openUntil time.Time
}

// Record adds an outcome; it reports whether this one opened the breaker.
func (b *Breaker) Record(now time.Time, ok bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.window = append(b.window, ok)
	if len(b.window) > 100 {
		b.window = b.window[len(b.window)-100:]
	}
	if len(b.window) < 20 || now.Before(b.openUntil) {
		return false
	}
	fails := 0
	for _, x := range b.window {
		if !x {
			fails++
		}
	}
	if fails*5 > len(b.window) {
		b.openUntil = now.Add(30 * time.Minute)
		b.window = b.window[:0]
		return true
	}
	return false
}

// Open reports whether feed reads are paused.
func (b *Breaker) Open(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return now.Before(b.openUntil)
}
