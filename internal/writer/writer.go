// Package writer stores what the collectors read (plan §8.2 "writer"):
// upserts keyed by YouTube IDs (re-runs are safe), a snapshot of the
// numbers on every check, a change log of edited fields, and the keyword
// matcher run on all new text. Anything on the suppression list (erased)
// is dropped before it is written.
package writer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fill"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// ErrSuppressed: the item (or its channel) was erased; nothing was written.
var ErrSuppressed = errors.New("writer: erased item, not stored")

// Writer writes to Postgres.
type Writer struct {
	Pool *pgxpool.Pool
	Sup  *privacy.Suppressor // nil: no suppression list (tests only)
	KW   *kwstore.Live       // nil: no keyword matching
	Now  func() time.Time
	Fill *fill.Recorder // nil: fill rates not tracked
	// Media: queue thumbnail, avatar and banner downloads (when storage is
	// configured).
	Media bool
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Writer) suppressed(kind, id string) bool {
	return w.Sup != nil && w.Sup.Suppressed(kind, id)
}

// Match runs the keyword matcher on texts.
func (w *Writer) Match(texts ...string) (bool, []string) {
	if w.KW == nil {
		return false, nil
	}
	return w.KW.Matcher().Match(strings.Join(texts, "\n"))
}

func count(c ytparse.Count) (*int64, *string) {
	if !c.OK {
		return nil, nil
	}
	v, t := c.Min, c.Text
	return &v, &t
}

func str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Channel is what a channel check read.
type Channel struct {
	ID          string
	Handle      string
	Title       string
	Description string
	Keywords    string
	Country     string
	JoinedText  string
	AvatarURL   string
	BannerURL   string
	FamilySafe  bool
	Links       []ytparse.Link
	Subscribers ytparse.Count
	Videos      ytparse.Count
	Views       ytparse.Count
}

// ChannelFrom combines a channel page and its About panel.
func ChannelFrom(ch ytparse.Channel, about ytparse.About, hasAbout bool) Channel {
	c := Channel{ID: ch.ID, Handle: ch.Handle, Title: ch.Title, Description: ch.Description, Keywords: ch.Keywords,
		AvatarURL: ch.AvatarURL, BannerURL: ch.BannerURL, FamilySafe: ch.FamilySafe,
		Subscribers: ch.Subscribers, Videos: ch.VideoCount}
	if hasAbout {
		if about.Description != "" {
			c.Description = about.Description
		}
		c.Country, c.JoinedText, c.Links, c.Views = about.Country, about.JoinedText, about.Links, about.Views
		if about.Subscribers.OK {
			c.Subscribers = about.Subscribers
		}
		if about.Videos.OK {
			c.Videos = about.Videos
		}
	}
	return c
}

// Channel updates a listed channel, logs changed fields and takes a
// snapshot. It returns the names of the fields that changed.
func (w *Writer) Channel(ctx context.Context, c Channel) ([]string, error) {
	if w.suppressed(privacy.Channel, c.ID) {
		return nil, ErrSuppressed
	}
	w.Fill.Observe("channel", "title", c.Title != "")
	w.Fill.Observe("channel", "handle", c.Handle != "")
	w.Fill.Observe("channel", "subscribers", c.Subscribers.OK)
	w.Fill.Observe("channel", "videos", c.Videos.OK)
	now := w.now()
	links, _ := json.Marshal(c.Links)
	if c.Links == nil {
		links = []byte("[]")
	}
	subs, subsText := count(c.Subscribers)
	vids, _ := count(c.Videos)
	views, _ := count(c.Views)
	var changed []string
	err := pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		var old struct{ handle, title, desc, kw, country, avatar, banner *string }
		err := tx.QueryRow(ctx, `SELECT handle, title, description, keywords, country, avatar_url, banner_url
			FROM channels WHERE id = $1 FOR UPDATE`, c.ID).
			Scan(&old.handle, &old.title, &old.desc, &old.kw, &old.country, &old.avatar, &old.banner)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("writer: channel %s is not in the database", c.ID)
		}
		if err != nil {
			return err
		}
		for _, f := range []struct {
			name     string
			old, new *string
		}{
			{"handle", old.handle, str(c.Handle)}, {"title", old.title, str(c.Title)}, {"description", old.desc, str(c.Description)},
			{"keywords", old.kw, str(c.Keywords)}, {"country", old.country, str(c.Country)},
			{"avatar_url", old.avatar, str(c.AvatarURL)}, {"banner_url", old.banner, str(c.BannerURL)},
		} {
			if f.new == nil || f.old == nil || *f.old == *f.new {
				continue // first fill, or a field this page did not show
			}
			changed = append(changed, f.name)
			if _, err := tx.Exec(ctx, `INSERT INTO channel_changes (channel_id, changed_at, field, old_value, new_value)
				VALUES ($1, $2, $3, $4, $5)`, c.ID, now, f.name, *f.old, *f.new); err != nil {
				return err
			}
		}
		if w.Media {
			for _, m := range []struct {
				kind     string
				old, new *string
			}{{media.Avatar, old.avatar, str(c.AvatarURL)}, {media.Banner, old.banner, str(c.BannerURL)}} {
				if m.new != nil && (m.old == nil || *m.old != *m.new) {
					if err := media.Queue(ctx, tx, m.kind, c.ID, now); err != nil {
						return err
					}
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE channels SET
				handle = COALESCE($2, handle), title = COALESCE($3, title), description = COALESCE($4, description),
				keywords = COALESCE($5, keywords), country = COALESCE($6, country), joined_text = COALESCE($7, joined_text),
				avatar_url = COALESCE($8, avatar_url), banner_url = COALESCE($9, banner_url), family_safe = $10,
				links = CASE WHEN $11::jsonb = '[]'::jsonb THEN links ELSE $11::jsonb END,
				subscribers_min = COALESCE($12, subscribers_min), subscribers_text = COALESCE($13, subscribers_text),
				videos_min = COALESCE($14, videos_min), views_min = COALESCE($15, views_min),
				last_checked = $16, missed_checks = 0
			WHERE id = $1`,
			c.ID, str(c.Handle), str(c.Title), str(c.Description), str(c.Keywords), str(c.Country), str(c.JoinedText),
			str(c.AvatarURL), str(c.BannerURL), c.FamilySafe, links, subs, subsText, vids, views, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO channel_snapshots (channel_id, taken_at, subscribers_min, subscribers_text, videos_min, views_min)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, c.ID, now, subs, subsText, vids, views)
		return err
	})
	return changed, err
}

// ChannelMissing counts a check that found the channel gone. The second
// miss in a row marks it removed (its stored content is kept, D18) and
// returns true.
func (w *Writer) ChannelMissing(ctx context.Context, id string) (removed bool, err error) {
	var misses int
	err = w.Pool.QueryRow(ctx, `UPDATE channels SET missed_checks = missed_checks + 1, last_checked = $2,
			removed_at = CASE WHEN missed_checks + 1 >= 2 THEN COALESCE(removed_at, $2) ELSE removed_at END,
			status = CASE WHEN missed_checks + 1 >= 2 THEN 'removed' ELSE status END
		WHERE id = $1 RETURNING missed_checks`, id, w.now()).Scan(&misses)
	return misses >= 2, err
}

// WasLive is the listing kind of a past live stream (the Live tab lists
// them without a "live now" badge).
const WasLive ytparse.Kind = "was_live"

// Kind and live status from a listing or the player.
func kindOf(k ytparse.Kind) (kind, live string) {
	switch k {
	case ytparse.KindShort:
		return "short", "none"
	case ytparse.KindLive:
		return "live", "live"
	case ytparse.KindUpcoming:
		return "live", "upcoming"
	case WasLive:
		return "live", "was_live"
	default:
		return "video", "none"
	}
}

// Listed stores a video seen in a channel tab, if it is new. Existing
// videos are left to their own checks. It reports whether it was new.
func (w *Writer) Listed(ctx context.Context, channelID string, it ytparse.VideoItem) (bool, error) {
	return w.ListedVia(ctx, channelID, it, "web")
}

// ListedVia is Listed with the source that saw the video first (web, rss,
// websub); detection delay is measured per source.
func (w *Writer) ListedVia(ctx context.Context, channelID string, it ytparse.VideoItem, source string) (bool, error) {
	if it.VideoID == "" {
		return false, errors.New("writer: video without an ID")
	}
	if w.suppressed(privacy.Video, it.VideoID) || w.suppressed(privacy.Channel, channelID) {
		return false, ErrSuppressed
	}
	kind, live := kindOf(it.Kind)
	if source == "web" {
		w.Fill.Observe("list", "title", it.Title != "")
		w.Fill.Observe("list", "published", it.Published.OK || it.Kind == ytparse.KindShort)
	}
	relevant, kws := w.Match(it.Title)
	var published *time.Time
	var precision *int
	if it.Published.OK {
		published = &it.Published.At
		p := int(it.Published.Precision / time.Second)
		precision = &p
	}
	var dur *int
	if it.DurationSec > 0 {
		dur = &it.DurationSec
	}
	tag, err := w.Pool.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, title, published_at, published_precision_s, published_text,
			duration_s, views, live_status, relevant, matched_keywords, first_seen, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (id) DO NOTHING`,
		it.VideoID, channelID, kind, str(it.Title), published, precision, str(it.PublishedText), dur,
		nullCount(it.Views), live, relevant, nonNil(kws), w.now(), source)
	if err != nil {
		return false, err
	}
	isNew := tag.RowsAffected() == 1
	if isNew && w.Media {
		if err := media.QueueNew(ctx, w.Pool, media.Thumb, it.VideoID, w.now()); err != nil {
			return true, err
		}
	}
	return isNew, nil
}

func nullCount(c ytparse.Count) *int64 {
	if !c.OK {
		return nil
	}
	v := c.Min
	return &v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Video is what a video check read.
type Video struct {
	ID           string
	ChannelID    string
	Watch        ytparse.WatchInfo
	Player       ytparse.PlayerInfo
	HasPlayer    bool
	ListingKind  ytparse.Kind // from the tab, when known
	PublishedEst ytparse.TimeEstimate
}

// VideoOutcome says what the check found.
type VideoOutcome struct {
	Relevant    bool
	Keywords    []string
	Changed     []string
	Published   time.Time // best known; zero if unknown
	LiveStatus  string
	CommentsOff bool
}

// parsePublish reads the player's publish date (a date-time, or a date).
func parsePublish(s string) (time.Time, time.Duration, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, 0, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Add(12 * time.Hour), 12 * time.Hour, true
	}
	return time.Time{}, 0, false
}

// Video stores a video's details and a snapshot of its numbers, logs
// edited fields, and flags it relevant when its text matches the keywords.
func (w *Writer) Video(ctx context.Context, v Video) (VideoOutcome, error) {
	var out VideoOutcome
	if w.suppressed(privacy.Video, v.ID) || w.suppressed(privacy.Channel, v.ChannelID) {
		return out, ErrSuppressed
	}
	now := w.now()
	w.Fill.Observe("watch", "title", v.Watch.Title != "")
	w.Fill.Observe("watch", "views", v.Watch.Views.OK)
	w.Fill.Observe("watch", "likes", v.Watch.Likes.OK)
	w.Fill.Observe("watch", "comment_count", v.Watch.CommentCount.OK || v.Watch.CommentsToken == "")
	w.Fill.Observe("watch", "date_text", v.Watch.DateText != "")
	if v.HasPlayer {
		w.Fill.Observe("player", "publish_date", v.Player.PublishDate != "")
		w.Fill.Observe("player", "length", v.Player.LengthSec > 0)
		w.Fill.Observe("player", "category", v.Player.Category != "")
	}
	title, desc := v.Watch.Title, v.Watch.Description
	if title == "" {
		title = v.Player.Title
	}
	if desc == "" {
		desc = v.Player.Description
	}
	kind, live := kindOf(v.ListingKind)
	if v.HasPlayer {
		switch {
		case v.Player.IsLive:
			kind, live = "live", "live"
		case v.Player.WasLive:
			kind, live = "live", "was_live"
		}
	}
	if v.Watch.LiveViewers.OK {
		kind, live = "live", "live" // "… watching now"
	}
	out.LiveStatus = live
	var published *time.Time
	var precision *int
	if t, p, ok := parsePublish(v.Player.PublishDate); ok {
		published = &t
		s := int(p / time.Second)
		precision = &s
	} else if v.PublishedEst.OK {
		published = &v.PublishedEst.At
		s := int(v.PublishedEst.Precision / time.Second)
		precision = &s
	}
	if published != nil {
		out.Published = *published
	}
	var views *int64
	if v.HasPlayer && v.Player.Views > 0 {
		views = &v.Player.Views
	} else {
		views = nullCount(v.Watch.Views)
	}
	likes, likesText := count(v.Watch.Likes)
	comments, commentsText := count(v.Watch.CommentCount)
	out.CommentsOff = v.Watch.CommentsToken == "" && !v.Watch.CommentCount.OK
	var tags []string
	var category *string
	var dur *int
	var captions []string
	if v.HasPlayer {
		tags, category, captions = v.Player.Keywords, str(v.Player.Category), v.Player.CaptionLangs
		if v.Player.LengthSec > 0 {
			dur = &v.Player.LengthSec
		}
	}
	out.Relevant, out.Keywords = w.Match(title, desc, strings.Join(tags, " "))
	chapters, _ := json.Marshal(v.Watch.Chapters)
	if v.Watch.Chapters == nil {
		chapters = nil
	}
	var liveViewers *int64
	if live == "live" {
		liveViewers = nullCount(v.Watch.LiveViewers)
	}
	err := pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		var old struct {
			title, desc, cat *string
			tags             []string
			exists           bool
		}
		err := tx.QueryRow(ctx, `SELECT title, description, category, tags FROM videos WHERE id = $1 FOR UPDATE`, v.ID).
			Scan(&old.title, &old.desc, &old.cat, &old.tags)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if w.Media {
				if err := media.QueueNew(ctx, tx, media.Thumb, v.ID, now); err != nil {
					return err
				}
			}
		case err != nil:
			return err
		default:
			old.exists = true
		}
		if old.exists {
			for _, f := range []struct {
				name     string
				old, new *string
			}{
				{"title", old.title, str(title)}, {"description", old.desc, str(desc)}, {"category", old.cat, category},
			} {
				if f.new == nil || f.old == nil || *f.old == *f.new {
					continue
				}
				out.Changed = append(out.Changed, f.name)
				if _, err := tx.Exec(ctx, `INSERT INTO video_changes (video_id, changed_at, field, old_value, new_value)
					VALUES ($1, $2, $3, $4, $5)`, v.ID, now, f.name, *f.old, *f.new); err != nil {
					return err
				}
			}
			if v.HasPlayer && old.tags != nil && strings.Join(old.tags, "\x00") != strings.Join(tags, "\x00") {
				out.Changed = append(out.Changed, "tags")
				if _, err := tx.Exec(ctx, `INSERT INTO video_changes (video_id, changed_at, field, old_value, new_value)
					VALUES ($1, $2, 'tags', $3, $4)`, v.ID, now, strings.Join(old.tags, ", "), strings.Join(tags, ", ")); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, title, description, published_at, published_precision_s,
				published_text, duration_s, views, likes_min, likes_text, comments_min, comments_text, comments_disabled,
				tags, category, player_unavailable, live_status, relevant, matched_keywords, family_safe, caption_langs, chapters,
				first_seen, last_checked, missed_checks, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
				$25, $25, 0, 'web')
			ON CONFLICT (id) DO UPDATE SET
				kind = EXCLUDED.kind, title = COALESCE(EXCLUDED.title, videos.title),
				description = COALESCE(EXCLUDED.description, videos.description),
				published_at = CASE WHEN videos.published_precision_s IS NULL OR
					COALESCE(EXCLUDED.published_precision_s, 2147483647) < videos.published_precision_s
					THEN COALESCE(EXCLUDED.published_at, videos.published_at) ELSE videos.published_at END,
				published_precision_s = LEAST(COALESCE(EXCLUDED.published_precision_s, videos.published_precision_s),
					COALESCE(videos.published_precision_s, EXCLUDED.published_precision_s)),
				published_text = COALESCE(EXCLUDED.published_text, videos.published_text),
				duration_s = COALESCE(EXCLUDED.duration_s, videos.duration_s),
				views = COALESCE(EXCLUDED.views, videos.views),
				likes_min = COALESCE(EXCLUDED.likes_min, videos.likes_min), likes_text = COALESCE(EXCLUDED.likes_text, videos.likes_text),
				comments_min = COALESCE(EXCLUDED.comments_min, videos.comments_min),
				comments_text = COALESCE(EXCLUDED.comments_text, videos.comments_text),
				comments_disabled = EXCLUDED.comments_disabled,
				tags = COALESCE(EXCLUDED.tags, videos.tags), category = COALESCE(EXCLUDED.category, videos.category),
				player_unavailable = EXCLUDED.player_unavailable, live_status = EXCLUDED.live_status,
				relevant = EXCLUDED.relevant, matched_keywords = EXCLUDED.matched_keywords,
				family_safe = COALESCE(EXCLUDED.family_safe, videos.family_safe),
				caption_langs = COALESCE(EXCLUDED.caption_langs, videos.caption_langs),
				chapters = COALESCE(EXCLUDED.chapters, videos.chapters),
				last_checked = EXCLUDED.last_checked, missed_checks = 0, removed_at = NULL`,
			v.ID, v.ChannelID, kind, str(title), str(desc), published, precision, str(v.Watch.DateText), dur, views,
			likes, likesText, comments, commentsText, out.CommentsOff, tagsOrNil(v.HasPlayer, tags), category, !v.HasPlayer,
			live, out.Relevant, nonNil(out.Keywords), familySafe(v), captionsOrNil(v.HasPlayer, captions), chapters, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO video_snapshots (video_id, taken_at, views, likes_min, comments_min, live_viewers)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, v.ID, now, views, likes, comments, liveViewers)
		return err
	})
	return out, err
}

func tagsOrNil(has bool, t []string) []string {
	if !has {
		return nil
	}
	return nonNil(t)
}

func captionsOrNil(has bool, c []string) []string {
	if !has {
		return nil
	}
	return nonNil(c)
}

func familySafe(v Video) *bool {
	if !v.HasPlayer {
		return nil
	}
	b := v.Player.FamilySafe
	return &b
}

// VideoMissing counts a check that found the video gone; the second miss
// marks it removed (its stored content is kept, D18).
func (w *Writer) VideoMissing(ctx context.Context, id string) (removed bool, err error) {
	var misses int
	err = w.Pool.QueryRow(ctx, `UPDATE videos SET missed_checks = missed_checks + 1, last_checked = $2,
			removed_at = CASE WHEN missed_checks + 1 >= 2 THEN COALESCE(removed_at, $2) ELSE removed_at END
		WHERE id = $1 RETURNING missed_checks`, id, w.now()).Scan(&misses)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return misses >= 2, err
}
