// Package enrich reads commenters' public channel pages (plan §8.7a): name,
// handle, description, country, join date, links, subscriber and video
// counts, avatar and banner. It runs at the lowest priority (P4), so it
// only uses spare capacity, the most active commenters on Iraq-relevant
// content first; profiles are read again every 6 months for active
// commenters and yearly for the rest. A commenter whose channel has uploads
// of its own goes through the candidate check (D10).
package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
)

// Enricher reads commenter profiles.
type Enricher struct {
	Pool  *pgxpool.Pool
	Fetch *phase0.Collector
	Sup   *privacy.Suppressor
	Log   *slog.Logger
	Now   func() time.Time
	Media bool // queue avatar downloads (when storage is configured)
}

func (e *Enricher) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Handlers maps the enrichment job kind to its handler.
func (e *Enricher) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{queue.KindEnrich: e.enrich}
}

func (e *Enricher) enrich(ctx context.Context, j queue.Job) (time.Time, error) {
	id := j.EntityID
	if e.Sup != nil && e.Sup.Suppressed(privacy.Author, id) {
		return time.Time{}, nil
	}
	var lastSeen time.Time
	err := e.Pool.QueryRow(ctx, `SELECT last_seen FROM authors WHERE id = $1`, id).Scan(&lastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil // erased or never stored
	}
	if err != nil {
		return time.Time{}, err
	}
	active := e.now().Sub(lastSeen) < 180*24*time.Hour
	next := e.now().Add(policy.AuthorRefresh(active))
	var cost phase0.Cost
	ch, err := e.Fetch.Channel(ctx, id, &cost)
	if errors.Is(err, innertube.ErrNotFound) {
		_, err := e.Pool.Exec(ctx, `UPDATE authors SET enriched_at = $2 WHERE id = $1`, id, e.now())
		return next, err
	}
	if err != nil {
		return time.Time{}, err
	}
	links, _ := json.Marshal(ch.About.Links)
	subs := ch.About.Subscribers
	if !subs.OK {
		subs = ch.Channel.Subscribers
	}
	vids := ch.About.Videos
	if !vids.OK {
		vids = ch.Channel.VideoCount
	}
	desc := ch.About.Description
	if desc == "" {
		desc = ch.Channel.Description
	}
	var subsMin, vidsMin *int64
	if subs.OK {
		subsMin = &subs.Min
	}
	if vids.OK {
		vidsMin = &vids.Min
	}
	now := e.now()
	err = pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO author_profiles (author_id, fetched_at, handle, display_name, description, country,
				links, subscribers_min, videos_min, avatar_url, banner_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
			id, now, nz(ch.Channel.Handle), nz(ch.Channel.Title), nz(desc), nz(ch.About.Country), links, subsMin, vidsMin,
			nz(ch.Channel.AvatarURL), nz(ch.Channel.BannerURL)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE authors SET enriched_at = $2, handle = COALESCE($3, handle),
			display_name = COALESCE($4, display_name), avatar_url = COALESCE($5, avatar_url) WHERE id = $1`,
			id, now, nz(ch.Channel.Handle), nz(ch.Channel.Title), nz(ch.Channel.AvatarURL))
		return err
	})
	if err != nil {
		return time.Time{}, err
	}
	if e.Media && ch.Channel.AvatarURL != "" {
		if err := media.Queue(ctx, e.Pool, media.AuthorAvatar, id, now); err != nil {
			return time.Time{}, err
		}
	}
	// A commenter with uploads of their own may be an Iraq channel.
	_, hasVideos := ch.Channel.Tabs["videos"]
	if vids.OK && vids.Min > 0 || hasVideos {
		var status string
		err := e.Pool.QueryRow(ctx, `SELECT status FROM channels WHERE id = $1`, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) && (e.Sup == nil || !e.Sup.Suppressed(privacy.Channel, id)) {
			if _, err := e.Pool.Exec(ctx, `INSERT INTO candidates (channel_id, sources) VALUES ($1, ARRAY['commenter'])
				ON CONFLICT (channel_id) DO UPDATE SET last_found = now(),
					sources = CASE WHEN 'commenter' = ANY(candidates.sources) THEN candidates.sources ELSE array_append(candidates.sources, 'commenter') END`,
				id); err != nil {
				return time.Time{}, err
			}
			if _, err := queue.Ensure(ctx, e.Pool, queue.KindCandidate, id, id, queue.P3, now); err != nil {
				return time.Time{}, err
			}
		}
	}
	return next, nil
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Prioritise orders the enrichment backlog: commenters with the most
// comments on Iraq-relevant content come first (their jobs' due times move
// earlier by one minute per relevant comment, at most a week). It returns
// how many jobs it re-ordered.
func Prioritise(ctx context.Context, q db.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE schedule s SET due_at = now() - make_interval(mins => LEAST(a.relevant_comments, 10080)::int)
		FROM authors a
		WHERE s.kind = $1 AND s.entity_id = a.id AND s.last_ok IS NULL AND s.lease_until IS NULL AND a.relevant_comments > 0`,
		queue.KindEnrich)
	return tag.RowsAffected(), err
}
