package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
)

// Media kinds (the media table's kind, and the prefix of a media job's
// entity ID: "thumb:<video ID>").
const (
	Thumb        = "thumb"         // a video's thumbnail
	Avatar       = "avatar"        // a listed channel's avatar
	Banner       = "banner"        // a listed channel's banner
	AuthorAvatar = "author_avatar" // an enriched commenter's avatar
	PostImage    = "post_image"    // a community post's attached image (fix 21); one job for all of a post's images
)

// knownKinds are the image kinds this version knows.
var knownKinds = map[string]bool{Thumb: true, Avatar: true, Banner: true, AuthorAvatar: true, PostImage: true}

// MaxBytes is the largest image kept.
const MaxBytes = 8 << 20

// ThumbRecheck: a thumbnail is read when the video is first seen and once
// more after this (creators often change it in the first days).
const ThumbRecheck = 7 * 24 * time.Hour

// thumbWait: how long a missing thumbnail of a new video is retried.
const thumbWait = 24 * time.Hour

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// JobID is the media job's entity ID for kind and id.
func JobID(kind, id string) string { return kind + ":" + id }

// Queue asks for kind's image of id to be (re)read: now, at the lowest
// priority (P4). A job that is already queued keeps its place.
func Queue(ctx context.Context, q db.Querier, kind, id string, now time.Time) error {
	return queue.Put(ctx, q, queue.KindMedia, JobID(kind, id), "", queue.P4, now)
}

// QueueFor asks for kind's image of id to be (re)read now at prio, with
// the channel it belongs to, so the queue's tier order applies (the owner,
// D-061, 13 November: "youtube thumbnails turned on"): a new video's first
// thumbnail at P1, a backfilled video's at P3 (FirstThumbPriority), a
// listed channel's new or changed avatar or banner at P2. Images already
// queued keep their place, or move up; older ones stay in spare time (P4).
func QueueFor(ctx context.Context, q db.Querier, kind, id, channelID string, prio int, now time.Time) error {
	return queue.Put(ctx, q, queue.KindMedia, JobID(kind, id), channelID, prio, now)
}

// FirstThumbPriority is the priority of a video's first thumbnail, by the
// video's age: P1 while it is new (an unknown age, 0, counts as new), P3
// for an older one found by the backfill, as for its first comment scan.
func FirstThumbPriority(age time.Duration) int {
	if age < queue.NewFor {
		return queue.P1
	}
	return queue.P3
}

// ThumbRecheckPriority is the priority of a thumbnail's second reading, a
// week after the video was first seen (D-061).
const ThumbRecheckPriority = queue.P3

// QueueNew is Queue for a video seen for the first time: an existing job
// (or one already finished) is left alone. The video's first reading then
// moves it up (QueueFor with FirstThumbPriority).
func QueueNew(ctx context.Context, q db.Querier, kind, id string, now time.Time) error {
	_, err := queue.Ensure(ctx, q, queue.KindMedia, JobID(kind, id), "", queue.P4, now)
	return err
}

// Images fetches images (innertube.Client in production; only YouTube's
// image hosts are allowed).
type Images interface {
	Image(ctx context.Context, rawURL string) (*innertube.Response, error)
}

// Service downloads and stores images.
type Service struct {
	Pool    *pgxpool.Pool
	Images  Images
	Store   Store
	Sup     *privacy.Suppressor
	Log     *slog.Logger
	Now     func() time.Time
	CDNBase string // thumbnails; default https://i.ytimg.com
	// Hold are the image kinds not downloaded for now (the owner, D-052, 11
	// November: "Not now for thumbnails"). Their jobs stay queued and come
	// back every HoldRecheck, with no request, until the kind is released.
	Hold map[string]bool
}

// HoldRecheck is how long a held image job waits before it is looked at
// again (it makes no request while held).
const HoldRecheck = 7 * 24 * time.Hour

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) suppressed(kind, id string) bool {
	return s.Sup != nil && s.Sup.Suppressed(kind, id)
}

// Handlers maps the media job kind to its handler.
func (s *Service) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{queue.KindMedia: s.fetch}
}

// ThumbURL is a video's thumbnail (480×360, which every video has).
func (s *Service) ThumbURL(videoID string) string {
	base := s.CDNBase
	if base == "" {
		base = "https://i.ytimg.com"
	}
	return strings.TrimRight(base, "/") + "/vi/" + videoID + "/hqdefault.jpg"
}

// source finds what to download: the image URL, the channel it belongs to
// and, for thumbnails, when the video was first seen. ok is false when the
// item is gone (erased, never stored) or has no image.
func (s *Service) source(ctx context.Context, kind, id string) (src, channelID string, firstSeen time.Time, ok bool, err error) {
	var u *string
	switch kind {
	case Thumb:
		err = s.Pool.QueryRow(ctx, `SELECT channel_id, first_seen FROM videos WHERE id = $1`, id).Scan(&channelID, &firstSeen)
		if err == nil && s.suppressed(privacy.Channel, channelID) {
			return "", "", firstSeen, false, nil
		}
		src = s.ThumbURL(id)
	case Avatar:
		err = s.Pool.QueryRow(ctx, `SELECT avatar_url FROM channels WHERE id = $1`, id).Scan(&u)
		channelID = id
	case Banner:
		err = s.Pool.QueryRow(ctx, `SELECT banner_url FROM channels WHERE id = $1`, id).Scan(&u)
		channelID = id
	case AuthorAvatar:
		err = s.Pool.QueryRow(ctx, `SELECT avatar_url FROM authors WHERE id = $1`, id).Scan(&u)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", firstSeen, false, nil
	}
	if err != nil {
		return "", "", firstSeen, false, err
	}
	if u != nil {
		src = *u
	}
	if strings.HasPrefix(src, "//") {
		src = "https:" + src
	}
	return src, channelID, firstSeen, src != "", nil
}

func privacyKind(kind string) string {
	switch kind {
	case Thumb:
		return privacy.Video
	case AuthorAvatar:
		return privacy.Author
	case PostImage:
		return privacy.Post
	default:
		return privacy.Channel
	}
}

var extFor = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif", "image/avif": "avif"}

// fetch is the media job: download the image, store it if it is new.
func (s *Service) fetch(ctx context.Context, j queue.Job) (time.Time, error) {
	kind, id, _ := strings.Cut(j.EntityID, ":")
	if !knownKinds[kind] || !validID.MatchString(id) {
		return time.Time{}, nil // not a job this version knows: drop it
	}
	if s.Hold[kind] {
		return s.now().Add(HoldRecheck), nil // held: kept queued, not downloaded
	}
	if s.suppressed(privacyKind(kind), id) {
		return time.Time{}, nil
	}
	if kind == PostImage {
		return s.fetchPost(ctx, id)
	}
	src, channelID, firstSeen, ok, err := s.source(ctx, kind, id)
	if err != nil || !ok {
		return time.Time{}, err
	}
	now := s.now()
	next := time.Time{} // avatars and banners: done until the writer sees a new URL
	if kind == Thumb && now.Before(firstSeen.Add(ThumbRecheck)) {
		next = firstSeen.Add(ThumbRecheck)
	}
	if kind != Thumb {
		var latest string
		err := s.Pool.QueryRow(ctx, `SELECT source_url FROM media WHERE kind = $1 AND entity_id = $2
			ORDER BY last_seen DESC LIMIT 1`, kind, id).Scan(&latest)
		if err == nil && latest == src {
			return next, nil // this picture is already stored
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, err
		}
	}
	found, err := s.download(ctx, kind, id, channelID, src, now)
	if err != nil {
		return time.Time{}, err
	}
	if !found && kind == Thumb && now.Before(firstSeen.Add(thumbWait)) {
		return now.Add(time.Hour), nil // a brand-new video's thumbnail may not exist yet
	}
	// The recheck a week on is not a new video's thumbnail: P3, unless the
	// first reading itself ran in spare time (an older image).
	if kind == Thumb && !next.IsZero() && j.Priority < ThumbRecheckPriority {
		if err := queue.Reprioritize(ctx, s.Pool, j, ThumbRecheckPriority); err != nil {
			return time.Time{}, err
		}
	}
	return next, nil
}

// fetchPost downloads a community post's images (fix 21): each one not
// stored yet, by its address. The job is then done; the writer queues it
// again when the post shows new images.
func (s *Service) fetchPost(ctx context.Context, id string) (time.Time, error) {
	var channelID *string
	var urls []string
	err := s.Pool.QueryRow(ctx, `SELECT channel_id, image_urls FROM posts WHERE id = $1`, id).Scan(&channelID, &urls)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil // erased, or never stored
	}
	if err != nil {
		return time.Time{}, err
	}
	ch := ""
	if channelID != nil {
		ch = *channelID
	}
	if ch != "" && s.suppressed(privacy.Channel, ch) {
		return time.Time{}, nil
	}
	now := s.now()
	for _, src := range urls {
		if strings.HasPrefix(src, "//") {
			src = "https:" + src
		}
		var known bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media WHERE kind = $1 AND entity_id = $2 AND source_url = $3)`,
			PostImage, id, src).Scan(&known); err != nil {
			return time.Time{}, err
		}
		if known {
			continue
		}
		if _, err := s.download(ctx, PostImage, id, ch, src, now); err != nil {
			return time.Time{}, err
		}
	}
	return time.Time{}, nil
}

// download fetches src and stores it as kind's image of id: a new picture
// gets its file, its row and its record event; a known one (same content)
// only has its last sighting moved. found is false when the image does not
// exist.
func (s *Service) download(ctx context.Context, kind, id, channelID, src string, now time.Time) (found bool, err error) {
	resp, err := s.Images.Image(ctx, src)
	if errors.Is(err, innertube.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if resp.Status != http.StatusOK {
		return false, fmt.Errorf("media: HTTP %d", resp.Status)
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	ext, isImage := extFor[ct]
	if !isImage {
		return true, fmt.Errorf("media: answer is %q, not an image", ct)
	}
	if len(resp.Body) == 0 || len(resp.Body) > MaxBytes {
		return true, fmt.Errorf("media: image of %d bytes", len(resp.Body))
	}
	sum := sha256.Sum256(resp.Body)
	hash := hex.EncodeToString(sum[:])
	tag, err := s.Pool.Exec(ctx, `UPDATE media SET last_seen = $4, source_url = $5 WHERE kind = $1 AND entity_id = $2 AND sha256 = $3`,
		kind, id, hash, now, src)
	if err != nil {
		return true, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil // same picture as before
	}
	key := fmt.Sprintf("media/%s/%s/%s.%s", kind, id, hash[:16], ext)
	if err := s.Store.Put(ctx, key, ct, resp.Body); err != nil {
		return true, err
	}
	var ch *string
	if channelID != "" {
		ch = &channelID
	}
	// The row and its record event (fix 12) in one transaction.
	ev := outbox.New(ctx, now)
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var inserted bool
		if err := tx.QueryRow(ctx, `INSERT INTO media (kind, entity_id, channel_id, sha256, object_key, source_url, content_type, bytes, first_seen, last_seen)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9) ON CONFLICT (kind, entity_id, sha256) DO UPDATE SET last_seen = EXCLUDED.last_seen
			RETURNING (xmax = 0)`, kind, id, ch, hash, key, src, ct, len(resp.Body), now).Scan(&inserted); err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		_, err := ev.MediaEvent(ctx, tx, outbox.KindMedia, outbox.MediaKey{Kind: kind, EntityID: id, SHA256: hash})
		return err
	})
	return true, err
}

// HandoffKinds are the images announced to analysis and deleted once it is
// done, by default (the owner: thumbnails, kept until analysed; on 11
// November, channel avatars and banners too; on 13 November, "all platforms
// media", so community post images too: fix 21). Commenters' avatars are
// kept unless listed (the relay's -media-kinds).
var HandoffKinds = []string{Thumb, Avatar, Banner, PostImage}

// ParseHold reads a comma-separated list of image kinds held from
// download (the worker's -media-hold); empty holds none.
func ParseHold(s string) (map[string]bool, error) {
	hold := map[string]bool{}
	if strings.TrimSpace(s) == "" {
		return hold, nil
	}
	kinds, err := ParseKinds(s)
	if err != nil {
		return nil, err
	}
	for _, k := range kinds {
		hold[k] = true
	}
	return hold, nil
}

// ParseKinds reads a comma-separated list of image kinds to hand off.
func ParseKinds(s string) ([]string, error) {
	var kinds []string
	seen := map[string]bool{}
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		if !knownKinds[k] {
			return nil, fmt.Errorf("media: %q is not an image kind (%s, %s, %s, %s or %s)", k, Thumb, Avatar, Banner, AuthorAvatar, PostImage)
		}
		seen[k] = true
		kinds = append(kinds, k)
	}
	if len(kinds) == 0 {
		return nil, fmt.Errorf("media: no image kind to hand off in %q", s)
	}
	return kinds, nil
}

// Location is where a store keeps objects, for the hand-off's messages:
// the bucket, and the prefix in front of every key.
func Location(s Store) (bucket, prefix string) {
	switch st := s.(type) {
	case *S3Store:
		return st.Bucket, st.Prefix
	case FileStore:
		return "file:" + st.Dir, st.Prefix
	case *FileStore:
		return "file:" + st.Dir, st.Prefix
	}
	return "", ""
}

// Keys lists the stored objects of an erasure target (privacy kind and ID):
// those still in storage (not deleted after the hand-off).
func Keys(ctx context.Context, q db.Querier, kind, id string) ([]string, error) {
	var sql string
	switch kind {
	case privacy.Author:
		sql = `SELECT object_key FROM media WHERE kind = 'author_avatar' AND entity_id = $1 AND deleted_at IS NULL`
	case privacy.Channel:
		sql = `SELECT object_key FROM media WHERE kind IN ('avatar', 'banner', 'thumb', 'post_image') AND (entity_id = $1 OR channel_id = $1)
			AND deleted_at IS NULL`
	case privacy.Video:
		sql = `SELECT object_key FROM media WHERE kind = 'thumb' AND entity_id = $1 AND deleted_at IS NULL`
	case privacy.Post:
		sql = `SELECT object_key FROM media WHERE kind = 'post_image' AND entity_id = $1 AND deleted_at IS NULL`
	default:
		return nil, nil
	}
	rows, err := q.Query(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
