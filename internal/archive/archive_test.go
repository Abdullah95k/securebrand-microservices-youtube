package archive_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/archive"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

const (
	ch      = "UCfakeARCHIVECHANNEL0001"
	authorX = "UCfakeARCHIVEAUTHORX0001"
	authorY = "UCfakeARCHIVEAUTHORY0001"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func exec(t *testing.T, pool *pgxpool.Pool, sqls ...string) {
	t.Helper()
	for _, s := range sqls {
		if _, err := pool.Exec(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func partitionExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	return count(t, pool, `SELECT count(*) FROM pg_class WHERE relname = $1 AND relnamespace = current_schema()::regnamespace`, name) == 1
}

func TestArchiveCycle(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if err := db.EnsurePartitions(ctx, pool, now, 1); err != nil {
		t.Fatal(err)
	}
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	exec(t, pool,
		`INSERT INTO videos (id, channel_id, kind, title) VALUES ('fakeAv1', '`+ch+`', 'video', 't')`,
		`INSERT INTO authors (id) VALUES ('`+authorX+`'), ('`+authorY+`')`,
		`INSERT INTO comments (id, parent_kind, parent_id, channel_id, author_id, text, text_hash, first_seen, last_seen) VALUES
			('fakeAc1', 'video', 'fakeAv1', '`+ch+`', '`+authorX+`', 'x', '\x00', now(), now()),
			('fakeAc2', 'video', 'fakeAv1', '`+ch+`', '`+authorY+`', 'y', '\x00', now(), now()),
			('fakeAc3', 'video', 'fakeAv1', '`+ch+`', '`+authorY+`', 'y2', '\x00', now(), now())`,
		// June 2025: old enough to archive (and in the default partition:
		// that month never had its own).
		`INSERT INTO channel_snapshots (channel_id, taken_at, subscribers_min) VALUES
			('`+ch+`', '2025-06-15T00:00:00Z', 100), ('`+ch+`', '2025-06-16T00:00:00Z', 101)`,
		`INSERT INTO video_snapshots (video_id, taken_at, views) VALUES ('fakeAv1', '2025-06-10T00:00:00Z', 5)`,
		`INSERT INTO comment_versions (comment_id, version, seen_at, text) VALUES
			('fakeAc1', 1, '2025-06-20T00:00:00Z', 'x says'), ('fakeAc2', 1, '2025-06-21T00:00:00Z', 'y says'),
			('fakeAc3', 1, '2025-06-22T00:00:00Z', 'y again')`,
		`INSERT INTO author_profiles (author_id, fetched_at, display_name) VALUES ('`+authorX+`', '2025-06-05T00:00:00Z', 'X')`,
		// Recent rows stay in Postgres.
		`INSERT INTO channel_snapshots (channel_id, taken_at, subscribers_min) VALUES ('`+ch+`', '2026-11-02T00:00:00Z', 200)`,
	)
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	// fakeAc3 was erased, but its rows are not deleted yet: it must not
	// reach the archive.
	if err := sup.Add(ctx, pool, privacy.Comment, "fakeAc3", "test", nil); err != nil {
		t.Fatal(err)
	}
	store := media.FileStore{Dir: t.TempDir()}
	a := &archive.Archiver{Pool: pool, Store: store, Sup: privacy.NewSuppressor(k), Log: quiet, Now: func() time.Time { return now }, PartRows: 1}

	res, err := a.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]archive.Result{}
	for _, r := range res {
		got[r.Table] = r
	}
	if r := got["comment_versions"]; r.Rows != 2 || r.Skipped != 1 || r.Parts != 2 {
		t.Errorf("comment versions: %+v (want 2 rows in 2 parts, the erased one left out)", r)
	}
	if r := got["channel_snapshots"]; r.Rows != 2 || !r.Month.Equal(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("channel snapshots: %+v", r)
	}
	for _, p := range []string{"channel_snapshots_y2025m06", "comment_versions_y2025m06", "author_profiles_y2025m06"} {
		if partitionExists(t, pool, p) {
			t.Errorf("%s must be dropped once archived", p)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM channel_snapshots`); n != 1 {
		t.Errorf("%d channel snapshots left; the recent one stays", n)
	}
	if n := count(t, pool, `SELECT sum(rows)::int FROM archive_months WHERE table_name = 'comment_versions'`); n != 2 {
		t.Errorf("archive_months rows %d", n)
	}

	// Search by ID.
	var out bytes.Buffer
	n, err := a.Search(ctx, archive.Query{Table: "comment_versions", IDs: map[string]string{"author": authorX}}, &out)
	if err != nil || n != 1 || !strings.Contains(out.String(), `"x says"`) || !strings.Contains(out.String(), `"video": "fakeAv1"`) {
		t.Errorf("search: %d %v %s", n, err, out.String())
	}

	// X is erased after archiving: the scrub rewrites the parts without X.
	if err := sup.Add(ctx, pool, privacy.Author, authorX, "test", nil); err != nil {
		t.Fatal(err)
	}
	dropped, err := a.Scrub(ctx)
	if err != nil || dropped != 2 {
		t.Fatalf("scrub removed %d rows (want X's comment version and profile): %v", dropped, err)
	}
	out.Reset()
	if n, _ := a.Search(ctx, archive.Query{Table: "comment_versions", IDs: map[string]string{"author": authorX}}, &out); n != 0 {
		t.Error("erased rows must be gone from the archive")
	}
	if again, err := a.Scrub(ctx); err != nil || again != 0 {
		t.Errorf("a second scrub finds nothing: %d %v", again, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM erasure_audit WHERE step LIKE 'archive %'`); n != 2 {
		t.Errorf("%d archive scrub audit rows", n)
	}

	// Restore puts the month back (without erased rows) for a while.
	june := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	restored, err := a.Restore(ctx, "comment_versions", june, now.Add(24*time.Hour))
	if err != nil || restored != 1 {
		t.Fatalf("restore: %d %v", restored, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM comment_versions WHERE comment_id = 'fakeAc2' AND text = 'y says'`); n != 1 {
		t.Error("the restored row must be back in Postgres")
	}
	if due, _ := a.Due(ctx, "comment_versions"); len(due) != 0 {
		t.Errorf("a restored month stays until its time is up: %v", due)
	}
	// Once the time is up it is archived again, its parts rewritten.
	a.Now = func() time.Time { return now.Add(48 * time.Hour) }
	if _, err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if partitionExists(t, pool, "comment_versions_y2025m06") {
		t.Error("re-archived after the restore period")
	}
	parts, _ := a.List(ctx)
	var cv []archive.Part
	for _, p := range parts {
		if p.Table == "comment_versions" {
			cv = append(cv, p)
		}
	}
	if len(cv) != 1 || cv[0].Rows != 1 {
		t.Errorf("comment version parts after re-archiving: %+v", cv)
	}
	if _, err := store.Get(ctx, archive.Key("comment_versions", june, 2)); err != media.ErrNotFound {
		t.Error("the superseded second part must be deleted")
	}

	// A row that turns up later for an archived month is added as a new
	// part; the old parts are kept as they are.
	exec(t, pool, `INSERT INTO channel_snapshots (channel_id, taken_at, subscribers_min) VALUES ('`+ch+`', '2025-06-30T00:00:00Z', 102)`)
	if _, err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM archive_index WHERE table_name = 'channel_snapshots'`); n != 3 {
		t.Errorf("%d channel snapshot parts; want the two first ones and one more", n)
	}
	if n := count(t, pool, `SELECT rows::int FROM archive_months WHERE table_name = 'channel_snapshots'`); n != 3 {
		t.Errorf("channel snapshot month rows %d", n)
	}

	// A damaged part is never rewritten from.
	if err := store.Put(ctx, archive.Key("channel_snapshots", june, 1), "application/gzip", []byte("damaged")); err != nil {
		t.Fatal(err)
	}
	if err := sup.Add(ctx, pool, privacy.Channel, "UCfakeSOMEOTHERCHANNEL01", "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Scrub(ctx); err == nil {
		t.Error("a part that does not match its checksum must stop the scrub")
	}
}
