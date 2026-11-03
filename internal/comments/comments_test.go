package comments_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/comments"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

const (
	ch     = "UCfakeCOMMENTS0000000001"
	vid    = "fakeCMv1"
	erased = "UCfakeERASEDAUTHOR000001"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func author(i int) string { return fmt.Sprintf("UCfakeAUTHOR%012d", i) }

func fakeComments(n int) []fakeyt.FakeComment {
	var cs []fakeyt.FakeComment
	for i := 0; i < n; i++ {
		cs = append(cs, fakeyt.FakeComment{ID: fmt.Sprintf("fakeC%02d", i), Text: fmt.Sprintf("comment %d", i),
			Author: author(i), Ago: fmt.Sprintf("%d minutes ago", 10+i), Likes: "3"})
	}
	return cs
}

type rig struct {
	yt   *fakeyt.Server
	pool *pgxpool.Pool
	eng  *comments.Engine
	ch   fakeyt.FakeChannel
}

func (r *rig) serve(cs []fakeyt.FakeComment) {
	r.ch.Videos[0].Comments = cs
	r.ch.Videos[0].CommentsTxt = fmt.Sprint(len(cs) + replies(cs))
	r.yt.ServeChannels([]fakeyt.FakeChannel{r.ch}, 2, 2)
}

func replies(cs []fakeyt.FakeComment) int {
	n := 0
	for _, c := range cs {
		n += len(c.Replies)
	}
	return n
}

func newRig(t *testing.T) *rig {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	px := fakeyt.NewForwardProxy("a")
	t.Cleanup(px.Close)
	ps, _ := proxypool.ParseList([]string{px.ProxyLine()})
	pp := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true}, 1)
	pool := dbtest.New(t)
	ctx := context.Background()
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.A}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, title, published_at, published_precision_s, relevant)
		VALUES ($1, $2, 'video', 'بغداد', now() - interval '2 hours', 0, true)`, vid, ch); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM schedule`)
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	if err := sup.Add(ctx, pool, privacy.Author, erased, "erasure request", nil); err != nil {
		t.Fatal(err)
	}
	r := &rig{yt: yt, pool: pool, ch: fakeyt.FakeChannel{ID: ch, Handle: "@fakecomments", Title: "c",
		Videos: []fakeyt.FakeVideo{{ID: vid, Title: "بغداد", Views: 1, Ago: "2 hours ago", LengthSec: 60}}},
		eng: &comments.Engine{Pool: pool, Client: innertube.New(innertube.Options{Pool: pp, BaseURL: yt.URL}), Sup: sup, Log: quiet}}
	return r
}

func (r *rig) run(t *testing.T, kind string) time.Time {
	t.Helper()
	next, err := r.eng.Handlers()[kind](context.Background(), queue.Job{Kind: kind, EntityID: vid, ChannelID: ch})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func (r *rig) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *rig) pagesRead(prefix string) int {
	n := 0
	for _, q := range r.yt.Requests() {
		if tok, _ := q.Body["continuation"].(string); strings.HasPrefix(tok, prefix) {
			n++
		}
	}
	return n
}

func TestFirstReadScansEditsRepliesAndDeletions(t *testing.T) {
	r := newRig(t)
	cs := fakeComments(10)
	cs[0].Pinned, cs[0].Hearted = true, true
	cs[1].Replies = []fakeyt.FakeComment{{ID: "fakeR1", Text: "reply one", Author: ch, Ago: "5 minutes ago", Likes: "1"}}
	cs[2].Author = erased
	r.serve(cs)

	// First read: everything, except the erased author's comment.
	next := r.run(t, queue.KindComments)
	if n := r.count(t, `SELECT count(*) FROM comments WHERE parent_id = $1`, vid); n != 10 {
		t.Fatalf("stored %d comments; want 9 top-level + 1 reply", n)
	}
	if n := r.count(t, `SELECT count(*) FROM comments WHERE author_id = $1`, erased); n != 0 {
		t.Error("an erased author's comment must not be stored")
	}
	if n := r.count(t, `SELECT count(*) FROM comments WHERE id = 'fakeR1' AND thread_id = 'fakeC01' AND author_is_creator`); n != 1 {
		t.Error("the reply must name its thread, and the creator's reply is marked")
	}
	if n := r.count(t, `SELECT count(*) FROM comments WHERE id = 'fakeC00' AND pinned AND hearted`); n != 1 {
		t.Error("pinned and hearted must be kept")
	}
	if n := r.count(t, `SELECT count(*) FROM comment_versions`); n != 10 {
		t.Error("every comment starts with version 1")
	}
	if n := r.count(t, `SELECT count(*) FROM authors`); n != 10 {
		t.Errorf("authors: %d", n)
	}
	if n := r.count(t, `SELECT count(*) FROM schedule WHERE kind = 'enrich' AND priority = 4`); n != 10 {
		t.Error("new commenters are queued for enrichment at P4")
	}
	if n := r.count(t, `SELECT count(*) FROM schedule WHERE kind = 'sweep'`); n != 1 {
		t.Error("the first full sweep must be scheduled")
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE id = $1 AND comments_complete_pct > 90 AND NOT comments_partial`, vid); n != 1 {
		t.Error("completeness must be recorded")
	}
	if d := time.Until(next); d < 55*time.Minute || d > 61*time.Minute {
		t.Errorf("next scan in %v; tier A, new video: 1 hour", d)
	}

	// New comment on top, an edit on page 2, a new reply: the scan stops early.
	cs = append([]fakeyt.FakeComment{{ID: "fakeCnew", Text: "fresh", Author: author(99), Ago: "1 minute ago", Likes: "0"}}, cs...)
	cs[3].Text = "comment 2 (edited) — not stored, erased author"
	cs[4].Text = "comment 3, edited"
	cs[2].Replies = append(cs[2].Replies, fakeyt.FakeComment{ID: "fakeR2", Text: "reply two", Author: author(50), Ago: "1 minute ago"})
	before := r.pagesRead("NEWEST_" + vid + "_4")
	r.serve(cs)
	r.run(t, queue.KindComments)
	if n := r.count(t, `SELECT count(*) FROM comments WHERE id IN ('fakeCnew', 'fakeR2')`); n != 2 {
		t.Error("the new comment and the new reply must be stored")
	}
	if n := r.count(t, `SELECT count(*) FROM comments WHERE id = 'fakeC03' AND version = 2 AND text = 'comment 3, edited'`); n != 1 {
		t.Error("an edit must become version 2")
	}
	if n := r.count(t, `SELECT count(*) FROM comment_versions WHERE comment_id = 'fakeC03'`); n != 2 {
		t.Error("both versions must be kept")
	}
	if r.pagesRead("NEWEST_"+vid+"_4") != before || r.pagesRead("NEWEST_"+vid+"_5") != 0 {
		t.Error("the scan must stop after the first all-known page plus one, not read to the end")
	}

	// One comment disappears: the first sweep marks it missed, the second deleted.
	gone := cs[6].ID
	r.serve(append(append([]fakeyt.FakeComment{}, cs[:6]...), cs[7:]...))
	r.run(t, queue.KindSweep)
	if n := r.count(t, `SELECT count(*) FROM comments WHERE id = $1 AND missed_sweeps = 1 AND deleted_at IS NULL`, gone); n != 1 {
		t.Error("one miss marks it missing, not deleted")
	}
	r.run(t, queue.KindSweep)
	if n := r.count(t, `SELECT count(*) FROM comments WHERE id = $1 AND deleted_at IS NOT NULL AND text <> ''`, gone); n != 1 {
		t.Error("the second miss marks it deleted, and its text is kept")
	}
	if n := r.count(t, `SELECT count(*) FROM comments WHERE deleted_at IS NOT NULL`); n != 1 {
		t.Error("only the missing comment may be marked deleted")
	}
}

func TestCapFlagsPartial(t *testing.T) {
	r := newRig(t)
	r.eng.Cap = 4
	r.serve(fakeComments(9))
	r.run(t, queue.KindComments)
	if n := r.count(t, `SELECT count(*) FROM comments`); n > 5 || n < 4 {
		t.Errorf("stored %d; the cap is 4 (a page may finish)", n)
	}
	if n := r.count(t, `SELECT count(*) FROM videos WHERE comments_partial`); n != 1 {
		t.Error("a capped video is flagged partial")
	}
}
