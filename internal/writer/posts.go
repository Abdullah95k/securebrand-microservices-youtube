package writer

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// PostOutcome says what storing a post found.
type PostOutcome struct {
	New      bool
	Relevant bool
	Changed  bool // the text was edited
}

// Post stores a community post with a snapshot of its numbers. An edited
// text replaces the stored one; the change is logged in video_changes
// under the post's ID (field "post_text"). With Media, a post whose images
// are new or changed has them queued for download (fix 21).
func (w *Writer) Post(ctx context.Context, channelID string, p ytparse.Post) (PostOutcome, error) {
	var out PostOutcome
	if p.ID == "" {
		return out, errors.New("writer: post without an ID")
	}
	if w.suppressed(privacy.Post, p.ID) || w.suppressed(privacy.Channel, channelID) {
		return out, ErrSuppressed
	}
	now := w.now()
	relevant, kws := w.Match(p.Text)
	out.Relevant = relevant
	var published *time.Time
	var precision *int
	if p.Published.OK {
		published = &p.Published.At
		s := int(p.Published.Precision / time.Second)
		precision = &s
	}
	likes, likesText := count(p.Likes)
	comments, commentsText := count(p.Comments)
	ev := outbox.New(ctx, now)
	err := pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		var oldText *string
		var oldImages []string
		err := tx.QueryRow(ctx, `SELECT text, image_urls FROM posts WHERE id = $1 FOR UPDATE`, p.ID).Scan(&oldText, &oldImages)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			out.New = true
		case err != nil:
			return err
		}
		if !out.New && oldText != nil && p.Text != "" && *oldText != p.Text {
			out.Changed = true
			if _, err := tx.Exec(ctx, `INSERT INTO video_changes (video_id, changed_at, field, old_value, new_value)
				VALUES ($1, $2, 'post_text', $3, $4)`, p.ID, now, *oldText, p.Text); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO posts (id, channel_id, text, published_at, published_precision_s, published_text,
				likes_min, likes_text, comments_min, comments_text, relevant, matched_keywords, detail_params,
				has_image, has_poll, has_video, first_seen, last_checked, source, image_urls)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $17, 'web', $18)
			ON CONFLICT (id) DO UPDATE SET text = COALESCE(EXCLUDED.text, posts.text),
				published_at = CASE WHEN posts.published_precision_s IS NULL
					OR COALESCE(EXCLUDED.published_precision_s, 2147483647) < posts.published_precision_s
					THEN COALESCE(EXCLUDED.published_at, posts.published_at) ELSE posts.published_at END,
				published_precision_s = LEAST(COALESCE(EXCLUDED.published_precision_s, posts.published_precision_s),
					COALESCE(posts.published_precision_s, EXCLUDED.published_precision_s)),
				likes_min = COALESCE(EXCLUDED.likes_min, posts.likes_min), likes_text = COALESCE(EXCLUDED.likes_text, posts.likes_text),
				comments_min = COALESCE(EXCLUDED.comments_min, posts.comments_min),
				comments_text = COALESCE(EXCLUDED.comments_text, posts.comments_text),
				relevant = EXCLUDED.relevant, matched_keywords = EXCLUDED.matched_keywords,
				detail_params = COALESCE(EXCLUDED.detail_params, posts.detail_params),
				image_urls = CASE WHEN cardinality(EXCLUDED.image_urls) > 0 THEN EXCLUDED.image_urls ELSE posts.image_urls END,
				last_checked = EXCLUDED.last_checked, removed_at = NULL`,
			p.ID, channelID, str(p.Text), published, precision, str(p.PublishedText), likes, likesText, comments, commentsText,
			out.Relevant, nonNil(kws), str(p.DetailParams), p.HasImage, p.HasPoll, p.HasVideo, now, nonNil(p.Images)); err != nil {
			return err
		}
		// A page that shows no images keeps the stored ones; new or changed
		// images are fetched (one job for all of a post's images).
		if w.Media && len(p.Images) > 0 && !slices.Equal(oldImages, p.Images) {
			if err := media.Queue(ctx, tx, media.PostImage, p.ID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO post_snapshots (post_id, taken_at, likes_min, comments_min) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, p.ID, now, likes, comments); err != nil {
			return err
		}
		return ev.Write(ctx, tx, outbox.Posts, p.ID)
	})
	return out, err
}

// Playlist stores a playlist card (title, video count, relevance).
func (w *Writer) Playlist(ctx context.Context, channelID string, p ytparse.Playlist) (relevant bool, err error) {
	if w.suppressed(privacy.Channel, channelID) {
		return false, ErrSuppressed
	}
	relevant, _ = w.Match(p.Title)
	var count *int
	if p.VideoCount.OK {
		n := int(p.VideoCount.Min)
		count = &n
	}
	now := w.now()
	ev := outbox.New(ctx, now)
	err = pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO playlists (id, channel_id, title, video_count, relevant, first_seen, last_checked)
			VALUES ($1, $2, $3, $4, $5, $6, $6)
			ON CONFLICT (id) DO UPDATE SET title = COALESCE(EXCLUDED.title, playlists.title),
				video_count = COALESCE(EXCLUDED.video_count, playlists.video_count), relevant = EXCLUDED.relevant,
				last_checked = EXCLUDED.last_checked, removed_at = NULL`,
			p.ID, channelID, str(p.Title), count, relevant, now); err != nil {
			return err
		}
		_, err := ev.Content(ctx, tx, outbox.Playlists, p.ID)
		return err
	})
	return relevant, err
}

// PlaylistItems stores a playlist's videos as read now; when complete (the
// whole list was read), videos no longer in it are marked removed.
func (w *Writer) PlaylistItems(ctx context.Context, playlistID string, items []ytparse.PlaylistEntry, complete bool) error {
	now := w.now()
	ev := outbox.New(ctx, now)
	return pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(items))
		for i, it := range items {
			if it.VideoID == "" || w.suppressed(privacy.Video, it.VideoID) {
				continue
			}
			pos := it.Position
			if pos == 0 {
				pos = i + 1
			}
			ids = append(ids, it.VideoID)
			if _, err := tx.Exec(ctx, `INSERT INTO playlist_items (playlist_id, video_id, position, first_seen, last_seen)
				VALUES ($1, $2, $3, $4, $4)
				ON CONFLICT (playlist_id, video_id) DO UPDATE SET position = EXCLUDED.position, last_seen = EXCLUDED.last_seen,
					removed_at = NULL`, playlistID, it.VideoID, pos, now); err != nil {
				return err
			}
		}
		if complete {
			if _, err := tx.Exec(ctx, `UPDATE playlist_items SET removed_at = $3
				WHERE playlist_id = $1 AND removed_at IS NULL AND NOT (video_id = ANY($2))`, playlistID, ids, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE playlists SET last_checked = $2 WHERE id = $1`, playlistID, now); err != nil {
			return err
		}
		_, err := ev.Content(ctx, tx, outbox.Playlists, playlistID) // the list changed
		return err
	})
}
