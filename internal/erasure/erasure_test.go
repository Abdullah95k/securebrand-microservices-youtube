package erasure_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/erasure"
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
