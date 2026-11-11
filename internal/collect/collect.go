// Package collect holds the job handlers that read YouTube and store what
// they find (plan §8.4): resolving channel inputs, channel pages and
// snapshots, the 365-day backfill, regular tab checks, and video details.
//
// Fetching reuses the client and parsers that Phase 0 measured against the
// real website (phase0.Collector: logged out, through the proxy pool, with
// its pacing, failover and block handling).
package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/rawstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// Service carries what the handlers need.
type Service struct {
	Pool       *pgxpool.Pool
	Fetch      *phase0.Collector
	Write      *writer.Writer
	Sup        *privacy.Suppressor
	Log        *slog.Logger
	Now        func() time.Time
	WithPlayer bool            // read the player endpoint for tags and category
	Window     time.Duration   // backfill depth (default 365 days)
	MaxPages   int             // safety limit per tab (default 400)
	Stretch    func() float64  // the capacity planner's stretch for C, D, M; nil = 1
	Raw        *rawstore.Store // keeps the pages behind a parse failure; nil = off

	recentTabs tabCache // tab parameters for RecentUploads
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) stretch(d time.Duration, t policy.Tier) time.Duration {
	if s.Stretch == nil {
		return d
	}
	return policy.Stretch(d, t, s.Stretch())
}

// Handlers maps job kinds to handlers.
func (s *Service) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{
		queue.KindResolve:   s.resolve,
		queue.KindChannel:   s.channel,
		queue.KindBackfill:  s.backfill,
		queue.KindTabs:      s.tabs,
		queue.KindVideo:     s.video,
		queue.KindPosts:     s.posts,
		queue.KindPlaylists: s.playlists,
	}
}

// retryable says whether an error is worth retrying: blocks, parked
// requests, network trouble and server errors are; "not found" is not.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, innertube.ErrNotFound) {
		return false
	}
	return errors.Is(err, innertube.ErrBlocked) || errors.Is(err, proxypool.ErrParked) || errors.Is(err, proxypool.ErrNoProxy) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Unwrap(err) != nil || strings.Contains(err.Error(), "HTTP 5")
}

// resolve turns a channel input into a channel ID and lists the channel.
func (s *Service) resolve(ctx context.Context, j queue.Job) (time.Time, error) {
	in, err := registry.PendingInput(ctx, s.Pool, j.EntityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil // the input was withdrawn
	}
	if err != nil {
		return time.Time{}, err
	}
	var cost phase0.Cost
	id, err := s.Fetch.Resolve(ctx, j.EntityID, &cost)
	if err != nil {
		if retryable(err) && j.Attempts < 5 {
			return time.Time{}, err
		}
		return time.Time{}, registry.InputResolved(ctx, s.Pool, j.EntityID, "", err)
	}
	if err := registry.List(ctx, s.Pool, s.Sup, id, in); err != nil {
		if errors.Is(err, registry.ErrSuppressed) {
			return time.Time{}, registry.InputResolved(ctx, s.Pool, j.EntityID, "", err)
		}
		return time.Time{}, err
	}
	return time.Time{}, registry.InputResolved(ctx, s.Pool, j.EntityID, id, nil)
}

type channelInfo struct {
	kind string
	tier policy.Tier
}

func (s *Service) channelInfo(ctx context.Context, id string) (channelInfo, bool, error) {
	var ci channelInfo
	var tier, status string
	err := s.Pool.QueryRow(ctx, `SELECT kind, tier, status FROM channels WHERE id = $1`, id).Scan(&ci.kind, &tier, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ci, false, nil
	}
	ci.tier = policy.Tier(tier)
	return ci, err == nil && status == "listed", err
}

// channel reads the channel page and About panel and stores a snapshot.
func (s *Service) channel(ctx context.Context, j queue.Job) (time.Time, error) {
	ci, listed, err := s.channelInfo(ctx, j.EntityID)
	if err != nil || !listed {
		return time.Time{}, err // not listed any more: drop the job
	}
	var cost phase0.Cost
	res, err := s.Fetch.Channel(ctx, j.EntityID, &cost)
	if errors.Is(err, innertube.ErrNotFound) || err == nil && res.Channel.ID == "" && res.Channel.Title == "" {
		removed, merr := s.Write.ChannelMissing(ctx, j.EntityID)
		if merr != nil {
			return time.Time{}, merr
		}
		if removed {
			s.Log.Info("channel gone; marked removed, its data is kept", "channel", j.EntityID)
			_, err := queue.RemoveChannel(ctx, s.Pool, j.EntityID)
			return time.Time{}, err
		}
		return s.now().Add(time.Hour), nil // check once more soon
	}
	if err != nil {
		return time.Time{}, err
	}
	if res.Channel.Title == "" {
		// Kept as a parse failure, with the raw answer, so the page can be
		// looked at; the channel is still stored, and its event is partial.
		msg := "channel page without a title"
		if len(res.Channel.Tabs) == 0 {
			msg += " or tabs"
		}
		s.parseFailure(ctx, "browse:channel", "channel", j.EntityID, msg)
	}
	if res.Channel.ID == "" {
		res.Channel.ID = j.EntityID
	}
	if _, err := s.Write.Channel(ctx, writer.ChannelFrom(res.Channel, res.About, res.HasAbout)); err != nil {
		if errors.Is(err, writer.ErrSuppressed) {
			return time.Time{}, registry.Unlist(ctx, s.Pool, j.EntityID, "erased")
		}
		return time.Time{}, err
	}
	return s.now().Add(s.stretch(policy.ChannelSnapshot(ci.tier), ci.tier)), nil
}

// tabKeys are the tabs that hold uploads (plan: Videos, Shorts, Live).
var tabKeys = []string{"videos", "shorts", "streams"}

func listingKind(tab string, it ytparse.VideoItem) ytparse.Kind {
	if it.Kind != "" && it.Kind != ytparse.KindVideo {
		return it.Kind
	}
	switch tab {
	case "shorts":
		return ytparse.KindShort
	case "streams":
		return writer.WasLive // no "live now" badge: a past broadcast
	}
	return ytparse.KindVideo
}

// backfill reads the last Window of uploads from the channel's tabs and
// queues every video found. It runs once per channel.
func (s *Service) backfill(ctx context.Context, j queue.Job) (time.Time, error) {
	ci, listed, err := s.channelInfo(ctx, j.EntityID)
	if err != nil || !listed || ci.kind != registry.Core {
		return time.Time{}, err
	}
	window, maxPages := s.Window, s.MaxPages
	if window <= 0 {
		window = 365 * 24 * time.Hour
	}
	if maxPages <= 0 {
		maxPages = 400
	}
	since := s.now().Add(-window)
	var cost phase0.Cost
	ch, err := s.Fetch.Channel(ctx, j.EntityID, &cost)
	if err != nil {
		return time.Time{}, err
	}
	found, added := 0, 0
	for _, key := range tabKeys {
		tab, ok := ch.Channel.Tabs[key]
		if !ok {
			continue
		}
		tr, err := s.Fetch.Tab(ctx, j.EntityID, tab.Params, since, maxPages, &cost)
		if err != nil {
			return time.Time{}, fmt.Errorf("backfill %s tab: %w", key, err)
		}
		for _, it := range tr.Items {
			if it.Published.OK && it.Published.At.Before(since) {
				continue
			}
			it.Kind = listingKind(key, it)
			isNew, err := s.Write.Listed(ctx, j.EntityID, it)
			if errors.Is(err, writer.ErrSuppressed) {
				continue
			}
			if err != nil {
				return time.Time{}, err
			}
			found++
			if isNew {
				added++
			}
			// Backfilled videos are read at P3; new uploads jump the queue elsewhere.
			if _, err := queue.Ensure(ctx, s.Pool, queue.KindVideo, it.VideoID, j.EntityID, queue.P3, s.now()); err != nil {
				return time.Time{}, err
			}
		}
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE channels SET backfill_until = $2, backfill_done_at = $3 WHERE id = $1`,
		j.EntityID, since, s.now()); err != nil {
		return time.Time{}, err
	}
	s.Log.Info("backfill done", "channel", j.EntityID, "videos", found, "new", added, "requests", cost.Requests)
	return time.Time{}, nil
}

// tabs reads the first pages of each upload tab until a page holds only
// known videos, and queues the new ones at P0.
func (s *Service) tabs(ctx context.Context, j queue.Job) (time.Time, error) {
	ci, listed, err := s.channelInfo(ctx, j.EntityID)
	if err != nil || !listed || ci.kind != registry.Core {
		return time.Time{}, err
	}
	var cost phase0.Cost
	ch, err := s.Fetch.Channel(ctx, j.EntityID, &cost)
	if err != nil {
		return time.Time{}, err
	}
	since := s.now().Add(-policy.TabCheck(ci.tier) - 7*24*time.Hour) // a week of overlap
	added := 0
	for _, key := range tabKeys {
		tab, ok := ch.Channel.Tabs[key]
		if !ok {
			continue
		}
		tr, err := s.Fetch.Tab(ctx, j.EntityID, tab.Params, since, 5, &cost)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s tab: %w", key, err)
		}
		for _, it := range tr.Items {
			it.Kind = listingKind(key, it)
			isNew, err := s.Write.Listed(ctx, j.EntityID, it)
			if errors.Is(err, writer.ErrSuppressed) {
				continue
			}
			if err != nil {
				return time.Time{}, err
			}
			if isNew {
				added++
				if err := queue.Now(ctx, s.Pool, queue.KindVideo, it.VideoID, j.EntityID, queue.P0); err != nil {
					return time.Time{}, err
				}
			}
		}
	}
	if added > 0 {
		s.Log.Info("tab check found new uploads", "channel", j.EntityID, "new", added)
	}
	return s.now().Add(policy.TabCheck(ci.tier)), nil // detection is never stretched
}

// video reads a video's watch page (and player), stores it with a
// snapshot, and schedules its next check and its comments.
func (s *Service) video(ctx context.Context, j queue.Job) (time.Time, error) {
	var channelID, kind, live string
	var published, lastChecked *time.Time
	var precision *int
	var category *string
	firstSeen := s.now()
	err := s.Pool.QueryRow(ctx, `SELECT channel_id, kind, COALESCE(live_status, 'none'), published_at, published_precision_s,
			last_checked, category, first_seen
		FROM videos WHERE id = $1`, j.EntityID).Scan(&channelID, &kind, &live, &published, &precision, &lastChecked, &category, &firstSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		channelID = j.ChannelID // first seen in a feed: the job names the channel
	} else if err != nil {
		return time.Time{}, err
	}
	ci, listed, err := s.channelInfo(ctx, channelID)
	if err != nil {
		return time.Time{}, err
	}
	if !listed {
		return time.Time{}, nil
	}
	// The player is guarded hardest (Phase 0 E1: blocked even at one
	// request per 10 s, IPs back after ~2 hours), and the watch page has
	// the numbers. Ask it only when it adds something: a video's first
	// check (exact publish time, category, tags), a stream that is or may
	// be live, and a first week's retry when it has not answered yet.
	usePlayer := s.WithPlayer && (lastChecked == nil || kind == "live" && (live == "live" || live == "upcoming") ||
		category == nil && s.now().Sub(firstSeen) < 7*24*time.Hour)
	var cost phase0.Cost
	res, err := s.Fetch.Video(ctx, j.EntityID, usePlayer, &cost)
	if err != nil && !errors.Is(err, innertube.ErrNotFound) {
		return time.Time{}, err
	}
	if err == nil && res.Watch.Title == "" && res.Player.Playability == "OK" {
		s.parseFailure(ctx, "next:watch", "video", j.EntityID, "watch page without a title while the player plays")
	}
	if err != nil || res.Watch.Title == "" && res.Player.Title == "" {
		removed, merr := s.Write.VideoMissing(ctx, j.EntityID)
		if merr != nil {
			return time.Time{}, merr
		}
		if removed {
			return time.Time{}, nil // gone: no more checks; its data is kept
		}
		return s.now().Add(time.Hour), nil
	}
	v := writer.Video{ID: j.EntityID, ChannelID: channelID, Watch: res.Watch, Player: res.Player,
		HasPlayer: res.PlayerErr == "" && res.Player.VideoID != "", SkippedPlayer: !usePlayer, ListingKind: ytparse.KindVideo}
	switch {
	case kind == "short":
		v.ListingKind = ytparse.KindShort
	case kind == "live" && live == "upcoming":
		v.ListingKind = ytparse.KindUpcoming
	case kind == "live":
		// Live at the last check, or a past stream: the watch page's
		// "watching now" or the player says whether it is live now.
		v.ListingKind = writer.WasLive
	}
	if published != nil {
		p := time.Duration(0)
		if precision != nil {
			p = time.Duration(*precision) * time.Second
		}
		v.PublishedEst = ytparse.TimeEstimate{At: *published, Precision: p, OK: true}
	}
	out, err := s.Write.Video(ctx, v)
	if errors.Is(err, writer.ErrSuppressed) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	// Mention channels: only Iraq-relevant items are collected in full.
	if ci.kind == registry.Mention && !out.Relevant {
		return time.Time{}, nil
	}
	age := time.Duration(0)
	if !out.Published.IsZero() {
		age = s.now().Sub(out.Published)
	}
	if !out.CommentsOff && policy.CommentScan(ci.tier, age) > 0 {
		prio := queue.P1
		if age > 2*24*time.Hour {
			prio = queue.P3 // backfilled videos: comments after the new ones
		}
		if _, err := queue.Ensure(ctx, s.Pool, queue.KindComments, j.EntityID, channelID, prio, s.now()); err != nil {
			return time.Time{}, err
		}
	}
	if out.LiveStatus == "live" {
		return s.now().Add(policy.LiveViewers(ci.tier)), nil
	}
	return s.now().Add(s.stretch(policy.VideoStats(ci.tier, age), ci.tier)), nil
}

// parseFailure records a page the parsers could not read (its scrubbed
// skeleton is in the debug folder); a rising rate raises an alert. The
// job's latest raw answers are kept forever, and raw_ref points to them.
func (s *Service) parseFailure(ctx context.Context, endpoint, kind, id, msg string) {
	refs := s.Raw.KeepRecent(ctx, "parse failure: "+endpoint+": "+msg)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO parse_failures (endpoint, entity_kind, entity_id, error, raw_ref) VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
		endpoint, kind, id, msg, strings.Join(refs, " ")); err != nil && s.Log != nil {
		s.Log.Warn("recording a parse failure failed", "err", err)
	}
}
