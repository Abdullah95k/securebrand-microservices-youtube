// Package comments keeps every video's comments complete and up to date
// (plan §8.7):
//
//   - the first read (backfill) takes every comment, newest first, up to a
//     cap (default 20,000), and flags capped videos "partial";
//   - new-comment scans read newest first and stop after the first page
//     whose comments are all known, plus one page of overlap;
//   - a thread whose reply count grew has its replies read again;
//   - a changed text is a new version (all versions are kept);
//   - full sweeps at the plan's ages mark a known comment missing when it
//     is not found; a second miss marks it deleted (text and author kept,
//     D18);
//   - completeness = collected ÷ the displayed count.
//
// Comments by erased authors, and erased comments, are dropped before they
// are stored. New commenters are queued for enrichment (P4).
package comments

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// DefaultCap is the most comments the first read of a video takes.
const DefaultCap = 20000

// Engine runs the comment jobs.
type Engine struct {
	Pool    *pgxpool.Pool
	Client  *innertube.Client
	Sup     *privacy.Suppressor
	Log     *slog.Logger
	Now     func() time.Time
	Cap     int            // 0 = DefaultCap
	Stretch func() float64 // the capacity planner's stretch for C, D, M
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) cap() int {
	if e.Cap <= 0 {
		return DefaultCap
	}
	return e.Cap
}

// Handlers maps the comment job kinds to their handlers.
func (e *Engine) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{
		queue.KindComments: e.scan,
		queue.KindSweep:    e.sweep,
	}
}

// known is what is stored about a comment.
type known struct {
	hash       []byte
	version    int
	replyCount int
	published  bool
	precision  int
}

type video struct {
	id, channelID string
	tier          policy.Tier
	channelKind   string
	relevant      bool
	published     time.Time
	hasPublished  bool
	everRead      bool // a first full read happened
}

func (e *Engine) loadVideo(ctx context.Context, id string) (video, bool, error) {
	v := video{id: id}
	var tier, status string
	var published *time.Time
	var complete *float32
	err := e.Pool.QueryRow(ctx, `SELECT v.channel_id, c.tier, c.kind, c.status, v.relevant, v.published_at, v.comments_complete_pct
		FROM videos v JOIN channels c ON c.id = v.channel_id WHERE v.id = $1`, id).
		Scan(&v.channelID, &tier, &v.channelKind, &status, &v.relevant, &published, &complete)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	v.tier = policy.Tier(tier)
	if published != nil {
		v.published, v.hasPublished = *published, true
	}
	v.everRead = complete != nil
	return v, status == "listed", nil
}

func (e *Engine) loadKnown(ctx context.Context, videoID string) (map[string]*known, error) {
	rows, err := e.Pool.Query(ctx, `SELECT id, text_hash, version, COALESCE(reply_count, 0), published_at IS NOT NULL,
		COALESCE(published_precision_s, 2147483647) FROM comments WHERE parent_kind = 'video' AND parent_id = $1`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*known{}
	for rows.Next() {
		var id string
		k := &known{}
		if err := rows.Scan(&id, &k.hash, &k.version, &k.replyCount, &k.published, &k.precision); err != nil {
			return nil, err
		}
		out[id] = k
	}
	return out, rows.Err()
}

// readResult is what a read of the comment section saw.
type readResult struct {
	displayed ytparse.Count
	seen      map[string]bool
	capped    bool
	complete  bool // read to the end (no early stop, not capped)
	pages     int
	stored    int
	newOnes   int
}

// read opens the comment section newest first and stores what it reads.
// With stopEarly (a new-comment scan), it stops after the first page whose
// top-level comments are all known (pinned ones aside), plus one more
// page, and re-reads replies only where a thread's reply count grew. A
// sweep (stopEarly false) reads every reply again.
func (e *Engine) read(ctx context.Context, v video, stopEarly bool) (readResult, error) {
	res := readResult{seen: map[string]bool{}}
	w, err := e.Client.Call(ctx, innertube.Next, map[string]any{"videoId": v.id})
	if err != nil {
		return res, err
	}
	watch := ytparse.ParseWatch(w.Data)
	if watch.CommentsToken == "" {
		res.complete = true // comments off, or none
		res.displayed = watch.CommentCount
		return res, nil
	}
	prefer := w.Proxy
	first, err := e.Client.CallPrefer(ctx, innertube.Next, map[string]any{"continuation": watch.CommentsToken}, prefer)
	if err != nil {
		return res, err
	}
	prefer = first.Proxy
	head := ytparse.ParseComments(first.Data, e.now())
	res.displayed = head.Total
	if !res.displayed.OK {
		res.displayed = watch.CommentCount
	}
	knownIDs, err := e.loadKnown(ctx, v.id)
	if err != nil {
		return res, err
	}
	token := head.NewestFirstToken
	pages := []ytparse.CommentsPage{}
	if token == "" {
		pages = append(pages, head) // no sort menu: the default order
		token = head.NextToken
	}
	allKnownPages := 0
	limit := e.cap()
	for {
		for _, p := range pages {
			n, fresh, err := e.storePage(ctx, v, p, knownIDs, res.seen, prefer, !stopEarly)
			if err != nil {
				return res, err
			}
			res.stored += n
			res.newOnes += fresh
			if stopEarly {
				if allTopKnown(p, knownIDs, fresh) {
					allKnownPages++
				} else {
					allKnownPages = 0
				}
				if allKnownPages >= 2 { // the first all-known page plus one page of overlap
					return res, nil
				}
			}
			if len(res.seen) >= limit {
				res.capped = true
				return res, nil
			}
		}
		pages = pages[:0]
		if token == "" {
			res.complete = true
			return res, nil
		}
		r, err := e.Client.CallPrefer(ctx, innertube.Next, map[string]any{"continuation": token}, prefer)
		if err != nil {
			return res, err
		}
		prefer = r.Proxy
		res.pages++
		p := ytparse.ParseComments(r.Data, e.now())
		pages = append(pages, p)
		token = p.NextToken
	}
}

// allTopKnown: every top-level comment on the page was known before this
// read (pinned comments sit on top whatever their age, so they don't count).
func allTopKnown(p ytparse.CommentsPage, knownBefore map[string]*known, fresh int) bool {
	if fresh > 0 {
		return false
	}
	any := false
	for _, c := range p.Comments {
		if c.IsReply || c.Pinned {
			continue
		}
		any = true
		if knownBefore[c.ID] == nil {
			return false
		}
	}
	return any
}

func textHash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// storePage stores one page and, for threads whose reply count grew, their
// replies. It returns how many comments it stored and how many were new.
func (e *Engine) storePage(ctx context.Context, v video, p ytparse.CommentsPage, knownIDs map[string]*known,
	seen map[string]bool, prefer string, allReplies bool) (stored, fresh int, err error) {
	var all []ytparse.Comment
	for _, c := range p.Comments {
		if c.ID == "" {
			continue
		}
		all = append(all, c)
		if c.IsReply {
			continue
		}
		k := knownIDs[c.ID]
		tok := p.ReplyTokens[c.ID]
		if tok != "" && c.ReplyCount > 0 && (allReplies || k == nil || int(c.ReplyCount) > k.replyCount) {
			replies, err := e.readReplies(ctx, tok, prefer)
			if err != nil {
				return stored, fresh, err
			}
			for i := range replies {
				if replies[i].ParentID == "" {
					replies[i].ParentID = c.ID
				}
				replies[i].IsReply = true
			}
			all = append(all, replies...)
		} else if k != nil && k.replyCount > 0 {
			// Replies not re-read (a scan, and the count did not grow):
			// they count as seen.
			e.markRepliesSeen(ctx, c.ID, seen)
		}
	}
	n, f, err := e.store(ctx, v, all, knownIDs, seen)
	return n, f, err
}

func (e *Engine) markRepliesSeen(ctx context.Context, threadID string, seen map[string]bool) {
	rows, err := e.Pool.Query(ctx, `SELECT id FROM comments WHERE thread_id = $1 AND deleted_at IS NULL`, threadID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			seen[id] = true
		}
	}
}

func (e *Engine) readReplies(ctx context.Context, token, prefer string) ([]ytparse.Comment, error) {
	var out []ytparse.Comment
	for token != "" {
		r, err := e.Client.CallPrefer(ctx, innertube.Next, map[string]any{"continuation": token}, prefer)
		if err != nil {
			return out, err
		}
		p := ytparse.ParseComments(r.Data, e.now())
		out = append(out, p.Comments...)
		token = p.NextToken
	}
	return out, nil
}

// store writes comments, their versions and their authors in one
// transaction.
func (e *Engine) store(ctx context.Context, v video, cs []ytparse.Comment, knownIDs map[string]*known, seen map[string]bool) (stored, fresh int, err error) {
	now := e.now()
	b := &pgx.Batch{}
	newAuthors := map[string]bool{}
	for _, c := range cs {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if e.Sup != nil && (e.Sup.Suppressed(privacy.Comment, c.ID) || e.Sup.Suppressed(privacy.Author, c.AuthorChannelID)) {
			continue
		}
		stored++
		h := textHash(c.Text)
		var thread *string
		if c.IsReply && c.ParentID != "" {
			thread = &c.ParentID
		}
		var published *time.Time
		var precision *int
		if c.Published.OK {
			published = &c.Published.At
			s := int(c.Published.Precision / time.Second)
			precision = &s
		}
		likes, likesText := countOf(c.Likes)
		badges := badgesJSON(c)
		k := knownIDs[c.ID]
		if k == nil {
			fresh++
			knownIDs[c.ID] = &known{hash: h, version: 1, replyCount: int(c.ReplyCount)}
			b.Queue(`INSERT INTO comments (id, parent_kind, parent_id, channel_id, thread_id, author_id, author_handle, author_name,
					author_avatar_url, author_is_creator, author_badges, text, text_hash, published_at, published_precision_s,
					likes_min, likes_text, reply_count, pinned, hearted, edited, version, first_seen, last_seen)
				VALUES ($1, 'video', $2, $3, $4, $5, $6, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, 1, $20, $20)
				ON CONFLICT (id) DO NOTHING`,
				c.ID, v.id, v.channelID, thread, nz(c.AuthorChannelID), nz(c.AuthorName), nz(c.AuthorAvatar),
				c.AuthorIsCreator || c.AuthorChannelID != "" && c.AuthorChannelID == v.channelID, badges, c.Text, h,
				published, precision, likes, likesText, c.ReplyCount, c.Pinned, c.Hearted, c.Edited, now)
			b.Queue(`INSERT INTO comment_versions (comment_id, version, seen_at, text, edited) VALUES ($1, 1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, c.ID, now, c.Text, c.Edited)
		} else {
			version := k.version
			if string(k.hash) != string(h) {
				version++
				k.version, k.hash = version, h
				b.Queue(`INSERT INTO comment_versions (comment_id, version, seen_at, text, edited) VALUES ($1, $2, $3, $4, $5)
					ON CONFLICT DO NOTHING`, c.ID, version, now, c.Text, c.Edited)
			}
			k.replyCount = int(c.ReplyCount)
			// A more precise publish time replaces a coarser one (a
			// backfilled "2 months ago" never overwrites "25 minutes ago").
			b.Queue(`UPDATE comments SET text = $2, text_hash = $3, version = $4, edited = edited OR $5,
					likes_min = COALESCE($6, likes_min), likes_text = COALESCE($7, likes_text), reply_count = $8,
					pinned = $9, hearted = $10, author_badges = COALESCE($11, author_badges), last_seen = $12, missed_sweeps = 0,
					published_at = CASE WHEN $13::timestamptz IS NOT NULL AND COALESCE(published_precision_s, 2147483647) > $14
						THEN $13 ELSE published_at END,
					published_precision_s = CASE WHEN $13::timestamptz IS NOT NULL AND COALESCE(published_precision_s, 2147483647) > $14
						THEN $14 ELSE published_precision_s END
				WHERE id = $1`,
				c.ID, c.Text, h, version, c.Edited, likes, likesText, c.ReplyCount, c.Pinned, c.Hearted, badges, now,
				published, precision)
		}
		if c.AuthorChannelID != "" {
			rel := 0
			if v.relevant {
				rel = 1
			}
			b.Queue(`INSERT INTO authors (id, handle, display_name, avatar_url, first_seen, last_seen, comments_seen, relevant_comments)
				VALUES ($1, $2, $2, $3, $4, $4, 1, $5)
				ON CONFLICT (id) DO UPDATE SET handle = COALESCE(EXCLUDED.handle, authors.handle),
					display_name = COALESCE(EXCLUDED.display_name, authors.display_name),
					avatar_url = COALESCE(EXCLUDED.avatar_url, authors.avatar_url), last_seen = EXCLUDED.last_seen,
					comments_seen = authors.comments_seen + CASE WHEN $6 THEN 1 ELSE 0 END,
					relevant_comments = authors.relevant_comments + CASE WHEN $6 THEN $5 ELSE 0 END`,
				c.AuthorChannelID, nz(c.AuthorName), nz(c.AuthorAvatar), now, rel, k == nil)
			if c.AuthorVerified {
				b.Queue(`INSERT INTO author_badges (author_id, badge, first_seen, last_seen) VALUES ($1, 'verified', $2, $2)
					ON CONFLICT (author_id, badge, channel_id) DO UPDATE SET last_seen = EXCLUDED.last_seen`, c.AuthorChannelID, now)
			}
			if c.AuthorBadge != "" {
				b.Queue(`INSERT INTO author_badges (author_id, badge, detail, channel_id, first_seen, last_seen) VALUES ($1, 'member', $2, $3, $4, $4)
					ON CONFLICT (author_id, badge, channel_id) DO UPDATE SET detail = EXCLUDED.detail, last_seen = EXCLUDED.last_seen`,
					c.AuthorChannelID, c.AuthorBadge, v.channelID, now)
			}
			if k == nil {
				newAuthors[c.AuthorChannelID] = true
			}
		}
	}
	if b.Len() == 0 {
		return stored, fresh, nil
	}
	err = pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.SendBatch(ctx, b).Close()
	})
	if err != nil {
		return stored, fresh, fmt.Errorf("storing comments: %w", err)
	}
	for a := range newAuthors {
		// Enrichment uses spare capacity only (P4); the job waits until then.
		if _, err := queue.Ensure(ctx, e.Pool, queue.KindEnrich, a, "", queue.P4, now); err != nil {
			return stored, fresh, err
		}
	}
	return stored, fresh, nil
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func countOf(c ytparse.Count) (*int64, *string) {
	if !c.OK {
		return nil, nil
	}
	v, t := c.Min, c.Text
	return &v, &t
}

func badgesJSON(c ytparse.Comment) []byte {
	if !c.AuthorVerified && c.AuthorBadge == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"verified": c.AuthorVerified, "member": c.AuthorBadge})
	return b
}

// finish records completeness and the partial flag.
func (e *Engine) finish(ctx context.Context, v video, res readResult, full bool) (float64, error) {
	var collected int64
	if err := e.Pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE parent_kind = 'video' AND parent_id = $1 AND deleted_at IS NULL`,
		v.id).Scan(&collected); err != nil {
		return 0, err
	}
	pct := 100.0
	if res.displayed.OK && res.displayed.Min > 0 {
		pct = 100 * float64(collected) / float64(res.displayed.Min)
		if pct > 100 {
			pct = 100
		}
	}
	_, err := e.Pool.Exec(ctx, `UPDATE videos SET comments_complete_pct = $2,
		comments_partial = CASE WHEN $3 THEN true WHEN $4 THEN false ELSE comments_partial END WHERE id = $1`,
		v.id, pct, res.capped, full && res.complete)
	return pct, err
}

func (e *Engine) age(v video) time.Duration {
	if !v.hasPublished {
		return 0
	}
	return e.now().Sub(v.published)
}

// scan is the newest-first scan; the first one reads everything.
func (e *Engine) scan(ctx context.Context, j queue.Job) (time.Time, error) {
	v, listed, err := e.loadVideo(ctx, j.EntityID)
	if err != nil || !listed {
		return time.Time{}, err
	}
	if e.Sup != nil && (e.Sup.Suppressed(privacy.Video, v.id) || e.Sup.Suppressed(privacy.Channel, v.channelID)) {
		return time.Time{}, nil
	}
	first := !v.everRead
	res, err := e.read(ctx, v, !first)
	if err != nil {
		return time.Time{}, err
	}
	pct, err := e.finish(ctx, v, res, first)
	if err != nil {
		return time.Time{}, err
	}
	age := e.age(v)
	if first {
		// The first full sweep at the plan's age (7 or 30 days).
		if s := policy.NextSweep(v.tier, age); s > 0 {
			due := e.now()
			if v.hasPublished {
				due = v.published.Add(s)
			}
			if _, err := queue.Ensure(ctx, e.Pool, queue.KindSweep, v.id, v.channelID, queue.P2, due); err != nil {
				return time.Time{}, err
			}
		}
	}
	if res.complete && pct < 90 && res.displayed.Min >= 20 {
		e.Log.Warn("comments below 90% complete; a full sweep is queued", "video", v.id, "complete_pct", int(pct))
		if err := queue.Put(ctx, e.Pool, queue.KindSweep, v.id, v.channelID, queue.P2, e.now().Add(time.Hour)); err != nil {
			return time.Time{}, err
		}
	}
	d := policy.CommentScan(v.tier, age)
	if d == 0 {
		return time.Time{}, nil // past the plan's last late-comment scan
	}
	if e.Stretch != nil {
		d = policy.Stretch(d, v.tier, e.Stretch())
	}
	return e.now().Add(d), nil
}

// sweep reads everything and counts known comments that were not found.
func (e *Engine) sweep(ctx context.Context, j queue.Job) (time.Time, error) {
	v, listed, err := e.loadVideo(ctx, j.EntityID)
	if err != nil || !listed {
		return time.Time{}, err
	}
	if e.Sup != nil && (e.Sup.Suppressed(privacy.Video, v.id) || e.Sup.Suppressed(privacy.Channel, v.channelID)) {
		return time.Time{}, nil
	}
	res, err := e.read(ctx, v, false)
	if err != nil {
		return time.Time{}, err
	}
	if res.complete && !res.capped {
		if err := e.markMissing(ctx, v.id, res.seen); err != nil {
			return time.Time{}, err
		}
	}
	if _, err := e.finish(ctx, v, res, true); err != nil {
		return time.Time{}, err
	}
	if s := policy.NextSweep(v.tier, e.age(v)); s > 0 && v.hasPublished {
		return v.published.Add(s), nil
	}
	return time.Time{}, nil
}

// markMissing: a known comment not found in a complete sweep is missed;
// the second miss in a row marks it deleted (kept, D18).
func (e *Engine) markMissing(ctx context.Context, videoID string, seen map[string]bool) error {
	rows, err := e.Pool.Query(ctx, `SELECT id FROM comments WHERE parent_kind = 'video' AND parent_id = $1 AND deleted_at IS NULL`, videoID)
	if err != nil {
		return err
	}
	var missing []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(missing) == 0 {
		return err
	}
	_, err = e.Pool.Exec(ctx, `UPDATE comments SET missed_sweeps = missed_sweeps + 1,
		deleted_at = CASE WHEN missed_sweeps + 1 >= 2 THEN $2::timestamptz ELSE NULL END
		WHERE id = ANY($1)`, missing, e.now())
	return err
}
