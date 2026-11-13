package erasure_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/erasure"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

const (
	ch      = "UCfakeERASURECHANNEL0001"
	authorX = "UCfakeERASUREAUTHORX0001"
	authorY = "UCfakeERASUREAUTHORY0001"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO videos (id, channel_id, kind, title) VALUES ('fakeEv1', '` + ch + `', 'video', 't')`,
		`INSERT INTO video_snapshots (video_id, taken_at, views) VALUES ('fakeEv1', now(), 1)`,
		`INSERT INTO posts (id, channel_id, text) VALUES ('UgkxFAKEERASE', '` + ch + `', 'p')`,
		`INSERT INTO authors (id, handle, display_name) VALUES ('` + authorX + `', '@xperson', '@xperson'), ('` + authorY + `', '@yperson', '@yperson')`,
		`INSERT INTO comments (id, parent_kind, parent_id, channel_id, author_id, author_handle, text, text_hash, first_seen, last_seen)
			VALUES ('fakeEc1', 'video', 'fakeEv1', '` + ch + `', '` + authorX + `', '@xperson', 'x says', '\x00', now(), now()),
			       ('fakeEc2', 'video', 'fakeEv1', '` + ch + `', '` + authorY + `', '@yperson', 'y says', '\x00', now(), now()),
			       ('fakeEc3', 'post', 'UgkxFAKEERASE', '` + ch + `', '` + authorX + `', '@xperson', 'x on post', '\x00', now() - interval '400 days', now())`,
		`INSERT INTO comment_versions (comment_id, version, seen_at, text) VALUES ('fakeEc1', 1, now(), 'x says'), ('fakeEc2', 1, now(), 'y says')`,
		`INSERT INTO author_badges (author_id, badge) VALUES ('` + authorX + `', 'verified')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Their outbox events (fix 12), as the writers add them.
	ev := outbox.New(ctx, time.Now())
	for tb, ids := range map[*outbox.Table][]string{&outbox.Channels: {ch}, &outbox.Videos: {"fakeEv1"}, &outbox.Posts: {"UgkxFAKEERASE"},
		&outbox.Comments: {"fakeEc1", "fakeEc2", "fakeEc3"}, &outbox.Authors: {authorX, authorY}} {
		if err := ev.Write(ctx, pool, *tb, ids...); err != nil {
			t.Fatal(err)
		}
	}
}

// outboxLeft lists the entities that still have outbox events, and those
// that still have outbox state.
func outboxLeft(t *testing.T, pool *pgxpool.Pool) (events, state string) {
	t.Helper()
	var e, st *string
	// Byte order (COLLATE "C"), as slices.Sort: not the database's locale.
	if err := pool.QueryRow(context.Background(), `SELECT
		(SELECT string_agg(DISTINCT entity_id COLLATE "C", ' ' ORDER BY entity_id COLLATE "C") FROM event_outbox),
		(SELECT string_agg(DISTINCT entity_id COLLATE "C", ' ' ORDER BY entity_id COLLATE "C") FROM outbox_state)`).Scan(&e, &st); err != nil {
		t.Fatal(err)
	}
	if e != nil {
		events = *e
	}
	if st != nil {
		state = *st
	}
	return events, state
}

func service(t *testing.T, pool *pgxpool.Pool) *erasure.Service {
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	return &erasure.Service{Pool: pool, Keyer: k, Sup: privacy.NewSuppressor(k), Log: quiet}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func runJob(t *testing.T, s *erasure.Service, id int64) {
	t.Helper()
	next, err := s.Handlers()[erasure.KindErasure](context.Background(), queue.Job{Kind: erasure.KindErasure, EntityID: itoa(id), Attempts: 1})
	if err != nil || !next.IsZero() {
		t.Fatalf("erasure job: next %v, err %v", next, err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestEraseAuthorByHandle(t *testing.T) {
	pool := dbtest.New(t)
	seed(t, pool)
	s := service(t, pool)
	ctx := context.Background()
	id, err := s.Request(ctx, privacy.Author, "https://www.youtube.com/@xperson", "ticket 17")
	if err != nil {
		t.Fatal(err)
	}
	runJob(t, s, id)
	if n := count(t, pool, `SELECT count(*) FROM comments WHERE author_id = $1`, authorX); n != 0 {
		t.Error("the author's comments (on videos and posts) must be gone")
	}
	if n := count(t, pool, `SELECT count(*) FROM comments WHERE author_id = $1`, authorY); n != 1 {
		t.Error("other people's comments stay")
	}
	if n := count(t, pool, `SELECT count(*) FROM comment_versions WHERE comment_id = 'fakeEc1'`); n != 0 {
		t.Error("versions go too")
	}
	if n := count(t, pool, `SELECT count(*) FROM authors WHERE id = $1`, authorX) + count(t, pool, `SELECT count(*) FROM author_badges`); n != 0 {
		t.Error("the author row and badges go")
	}
	if n := count(t, pool, `SELECT count(*) FROM erasure_requests WHERE id = $1 AND status = 'done' AND target IS NULL`, id); n != 1 {
		t.Error("a finished request keeps no raw target")
	}
	if n := count(t, pool, `SELECT count(*) FROM erasure_audit WHERE request_id = $1 AND step = 'comments' AND rows = 2`, id); n != 1 {
		t.Error("the audit log records row counts per step")
	}
	if !s.Sup.Suppressed(privacy.Author, authorX) {
		t.Error("the author is suppressed")
	}
	// Their events, and their comments' events, go too (unsent ones would
	// otherwise be published later).
	left := []string{ch, authorY, "UgkxFAKEERASE", "fakeEc2", "fakeEv1"}
	slices.Sort(left)
	want := strings.Join(left, " ")
	if ev, st := outboxLeft(t, pool); ev != want || st != want {
		t.Errorf("outbox events left for %q and state for %q, want %q", ev, st, want)
	}

	// A restore brings a comment back: the daily audit erases it again.
	_, _ = pool.Exec(ctx, `INSERT INTO comments (id, parent_kind, parent_id, channel_id, author_id, text, text_hash, first_seen, last_seen)
		VALUES ('fakeEc9', 'video', 'fakeEv1', $1, $2, 'back from a backup', '\x00', now(), now())`, ch, authorX)
	s2 := service(t, pool) // a new process: the list comes from the database
	res, err := s2.Audit(ctx)
	if err != nil || res.Total() != 1 {
		t.Fatalf("audit: %+v, err %v", res, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM comments WHERE author_id = $1`, authorX); n != 0 {
		t.Error("the audit must erase what came back")
	}
	if res, _ := s2.Audit(ctx); res.Total() != 0 {
		t.Error("a second audit finds nothing")
	}
}

func TestEraseChannel(t *testing.T) {
	pool := dbtest.New(t)
	seed(t, pool)
	s := service(t, pool)
	ctx := context.Background()
	id, err := s.Request(ctx, privacy.Channel, ch, "")
	if err != nil {
		t.Fatal(err)
	}
	runJob(t, s, id)
	for _, table := range []string{"channels", "videos", "video_snapshots", "posts", "comments", "comment_versions"} {
		if n := count(t, pool, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s still has %d row(s)", table, n)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE channel_id = $1 AND kind <> $2`, ch, erasure.KindErasure); n != 0 {
		t.Error("the channel's jobs must go")
	}
	// The channel's, its videos', posts' and comments' events go; the
	// commenters are not this erasure's.
	want := authorX + " " + authorY
	if ev, st := outboxLeft(t, pool); ev != want || st != want {
		t.Errorf("outbox events left for %q and state for %q, want %q", ev, st, want)
	}
	if err := registry.List(ctx, pool, s.Sup, ch, registry.Input{Input: ch, AddedBy: "discovery"}); !errors.Is(err, registry.ErrSuppressed) {
		t.Errorf("an erased channel must not be listed again: %v", err)
	}
}

func TestRetentionIsOffByDefault(t *testing.T) {
	pool := dbtest.New(t)
	seed(t, pool)
	ctx := context.Background()
	out, err := erasure.ApplyRetention(ctx, pool, privacy.Retention{}, time.Now())
	if err != nil || len(out) != 0 || count(t, pool, `SELECT count(*) FROM comments`) != 3 {
		t.Fatalf("default retention changed something: %v %v", out, err)
	}
	_, _ = pool.Exec(ctx, `UPDATE comments SET deleted_at = now() WHERE id = 'fakeEc2'`)
	out, err = erasure.ApplyRetention(ctx, pool, privacy.Retention{CommentsDays: 365, RedactDeletedComments: true}, time.Now())
	if err != nil || out["comments"] != 1 || out["redacted comments"] != 1 {
		t.Fatalf("retention: %v %v", out, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM comments WHERE id = 'fakeEc2' AND text = '' AND author_handle IS NULL`); n != 1 {
		t.Error("a deleted comment is redacted when that setting is on")
	}
}

// A community post's images (fix 21) go with the post, and with its
// channel; another post's stay.
func TestErasePostImages(t *testing.T) {
	pool := dbtest.New(t)
	seed(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO posts (id, channel_id, text) VALUES ('UgkxFAKEKEEP', $1, 'q')`, ch); err != nil {
		t.Fatal(err)
	}
	store := media.FileStore{Dir: t.TempDir()}
	for _, m := range []struct{ post, key string }{
		{"UgkxFAKEERASE", "media/post_image/UgkxFAKEERASE/aaaa.jpg"},
		{"UgkxFAKEERASE", "media/post_image/UgkxFAKEERASE/bbbb.jpg"},
		{"UgkxFAKEKEEP", "media/post_image/UgkxFAKEKEEP/cccc.jpg"},
	} {
		if err := store.Put(ctx, m.key, "image/jpeg", []byte("img")); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media (kind, entity_id, channel_id, sha256, object_key, source_url, content_type, bytes, first_seen, last_seen)
			VALUES ('post_image', $1, $2, $3, $3, 'https://yt3.ggpht.com/x', 'image/jpeg', 3, now(), now())`, m.post, ch, m.key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO schedule (kind, entity_id, priority, due_at) VALUES ('media', 'post_image:UgkxFAKEERASE', 4, now()),
		('media', 'post_image:UgkxFAKEKEEP', 4, now())`); err != nil {
		t.Fatal(err)
	}
	s := service(t, pool)
	s.Media = store
	id, err := s.Request(ctx, privacy.Post, "UgkxFAKEERASE", "")
	if err != nil {
		t.Fatal(err)
	}
	runJob(t, s, id)
	for _, k := range []string{"media/post_image/UgkxFAKEERASE/aaaa.jpg", "media/post_image/UgkxFAKEERASE/bbbb.jpg"} {
		if _, err := store.Get(ctx, k); !errors.Is(err, media.ErrNotFound) {
			t.Errorf("%s must be deleted: %v", k, err)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE entity_id = 'UgkxFAKEERASE'`); n != 0 {
		t.Errorf("%d of the erased post's image rows left", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE entity_id = 'post_image:UgkxFAKEERASE'`); n != 0 {
		t.Error("the erased post's image job must go")
	}
	if _, err := store.Get(ctx, "media/post_image/UgkxFAKEKEEP/cccc.jpg"); err != nil {
		t.Error("another post's image stays")
	}

	// Erasing the channel takes its other posts' images too, and the
	// Facebook links found on its page (D-065).
	if _, err := pool.Exec(ctx, `INSERT INTO fb_candidates (ref, channel_id, evidence_url, first_seen)
		VALUES ('facebook.com/fakeErasePage', $1, 'x', now())`, ch); err != nil {
		t.Fatal(err)
	}
	id, err = s.Request(ctx, privacy.Channel, ch, "")
	if err != nil {
		t.Fatal(err)
	}
	runJob(t, s, id)
	if _, err := store.Get(ctx, "media/post_image/UgkxFAKEKEEP/cccc.jpg"); !errors.Is(err, media.ErrNotFound) {
		t.Errorf("the channel's post image must be deleted: %v", err)
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE kind = 'post_image'`) + count(t, pool, `SELECT count(*) FROM schedule WHERE entity_id LIKE 'post_image:%'`); n != 0 {
		t.Errorf("%d post image rows or jobs left after the channel's erasure", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM fb_candidates`); n != 0 {
		t.Errorf("%d of the channel's Facebook links left", n)
	}
}

func TestEraseDeletesImages(t *testing.T) {
	pool := dbtest.New(t)
	seed(t, pool)
	ctx := context.Background()
	store := media.FileStore{Dir: t.TempDir()}
	for _, m := range []struct{ kind, entity, channel, key string }{
		{"avatar", ch, ch, "media/avatar/" + ch + "/aaaa.jpg"},
		{"thumb", "fakeEv1", ch, "media/thumb/fakeEv1/bbbb.jpg"},
		{"author_avatar", authorY, "", "media/author_avatar/" + authorY + "/cccc.jpg"},
	} {
		if err := store.Put(ctx, m.key, "image/jpeg", []byte("img")); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media (kind, entity_id, channel_id, sha256, object_key, source_url, content_type, bytes, first_seen, last_seen)
			VALUES ($1, $2, NULLIF($3, ''), $4, $4, 'https://i.ytimg.com/x', 'image/jpeg', 3, now(), now())`, m.kind, m.entity, m.channel, m.key); err != nil {
			t.Fatal(err)
		}
	}
	// A thumbnail already analysed and deleted from storage (its row stays),
	// the images' hand-off events, and an acknowledgement.
	if _, err := pool.Exec(ctx, `INSERT INTO media (kind, entity_id, channel_id, sha256, object_key, source_url, content_type, bytes,
		first_seen, last_seen, announced_at, delete_after, attempt, deleted_at, delete_reason)
		VALUES ('thumb', 'fakeEv1', $1, 'eeee', 'media/thumb/fakeEv1/eeee.jpg', 'x', 'image/jpeg', 1, now(), now(), now(), now(), 1, now(), 'processed')`,
		ch); err != nil {
		t.Fatal(err)
	}
	ev := outbox.New(ctx, time.Now())
	for _, k := range []outbox.MediaKey{{Kind: "avatar", EntityID: ch, SHA256: "media/avatar/" + ch + "/aaaa.jpg"},
		{Kind: "thumb", EntityID: "fakeEv1", SHA256: "eeee"}, {Kind: "author_avatar", EntityID: authorY, SHA256: "media/author_avatar/" + authorY + "/cccc.jpg"}} {
		for _, kind := range []string{outbox.KindMedia, outbox.KindMediaReady, outbox.KindMediaDeleted} {
			if n, err := ev.MediaEvent(ctx, pool, kind, k); err != nil || n != 1 {
				t.Fatalf("%s event: %d %v", kind, n, err)
			}
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_acks (kind, entity_id, sha256, consumer, status, received_at)
		VALUES ('thumb', 'fakeEv1', 'eeee', 'sb-analysis', 'processed', now())`); err != nil {
		t.Fatal(err)
	}

	// Without storage settings, an erasure with images fails (and retries)
	// rather than leaving the files behind.
	s := service(t, pool)
	if err := s.Erase(ctx, 0, privacy.Channel, ch); err == nil {
		t.Fatal("erasing images without storage must fail")
	}
	if n := count(t, pool, `SELECT count(*) FROM channels`); n != 1 {
		t.Fatal("a failed erasure must change nothing (it runs again)")
	}

	s.Media = store
	id, err := s.Request(ctx, privacy.Channel, ch, "")
	if err != nil {
		t.Fatal(err)
	}
	runJob(t, s, id)
	for _, k := range []string{"media/avatar/" + ch + "/aaaa.jpg", "media/thumb/fakeEv1/bbbb.jpg"} {
		if _, err := store.Get(ctx, k); !errors.Is(err, media.ErrNotFound) {
			t.Errorf("%s must be deleted: %v", k, err)
		}
	}
	if _, err := store.Get(ctx, "media/author_avatar/"+authorY+"/cccc.jpg"); err != nil {
		t.Error("a commenter's avatar is not the channel's: it stays")
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE channel_id = $1 OR entity_id = $1`, ch); n != 0 {
		t.Errorf("%d media rows left", n)
	}
	// Two files were in storage; the analysed thumbnail's was already gone.
	if n := count(t, pool, `SELECT count(*) FROM erasure_audit WHERE request_id = $1 AND step = 'media files' AND rows = 2`, id); n != 1 {
		t.Error("the audit log counts the deleted files")
	}
	if n := count(t, pool, `SELECT count(*) FROM event_outbox WHERE kind LIKE 'media%' AND entity_id NOT LIKE 'author_avatar:%'`); n != 0 {
		t.Errorf("%d of the channel's media events left", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM event_outbox WHERE kind LIKE 'media%' AND entity_id LIKE 'author_avatar:%'`); n != 3 {
		t.Errorf("the commenter's avatar events: %d, want them kept", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM media_acks`); n != 0 {
		t.Errorf("%d acknowledgements left", n)
	}

	// A thumbnail row that comes back (a restored backup) is erased again.
	_, _ = pool.Exec(ctx, `INSERT INTO media (kind, entity_id, channel_id, sha256, object_key, source_url, content_type, bytes, first_seen, last_seen)
		VALUES ('thumb', 'fakeEv1', $1, 'dd', 'media/thumb/fakeEv1/dd.jpg', 'x', 'image/jpeg', 1, now(), now())`, ch)
	res, err := s.Audit(ctx)
	if err != nil || res.Total() == 0 {
		t.Fatalf("audit: %+v %v", res, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE entity_id = 'fakeEv1'`); n != 0 {
		t.Error("the audit must erase images that came back")
	}
}
