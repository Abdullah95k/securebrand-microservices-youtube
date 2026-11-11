package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
)

func TestMigrateIsIdempotentAndCreatesPartitions(t *testing.T) {
	pool := dbtest.New(t) // already migrated once
	ctx := context.Background()
	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Errorf("second run applied %v", applied)
	}
	now := time.Now().UTC()
	for _, p := range db.Partitioned {
		for _, name := range []string{p.Table + "_default", db.PartitionName(p.Table, now), db.PartitionName(p.Table, now.AddDate(0, 3, 0))} {
			var ok bool
			if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil || !ok {
				t.Errorf("partition %s missing (err %v)", name, err)
			}
		}
	}
}

// TestNewPartitionTakesRowsFromDefault: a snapshot stored before its
// month's partition existed lands in the default partition and moves into
// the month's partition when that is created.
func TestNewPartitionTakesRowsFromDefault(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	far := time.Date(2031, 5, 10, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO video_snapshots (video_id, taken_at, views) VALUES ('fakeVID00001', $1, 7)`, far); err != nil {
		t.Fatal(err)
	}
	count := func(table string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("video_snapshots_default") != 1 {
		t.Fatal("the row should be in the default partition first")
	}
	if err := db.EnsurePartitions(ctx, pool, far, 0); err != nil {
		t.Fatal(err)
	}
	if count("video_snapshots_default") != 0 || count(db.PartitionName("video_snapshots", far)) != 1 || count("video_snapshots") != 1 {
		t.Error("the row should have moved into its month's partition")
	}
}

func TestSettings(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	var v struct{ Speed float64 }
	if ok, err := db.GetSetting(ctx, pool, "speed", &v); ok || err != nil {
		t.Fatalf("unset setting: ok %v, err %v", ok, err)
	}
	if err := db.SetSetting(ctx, pool, "speed", map[string]any{"Speed": 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, pool, "speed", map[string]any{"Speed": 0.75}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.GetSetting(ctx, pool, "speed", &v); !ok || err != nil || v.Speed != 0.75 {
		t.Errorf("got %v (ok %v, err %v)", v, ok, err)
	}
}

// Migration 17 (D-052) moves the queued feed and tab checks to P0 and
// leaves every other row as it is.
func TestFeedAndTabChecksMoveToP0(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at) VALUES
		('rss', 'UCfakeMIG17A', 'UCfakeMIG17A', 2, now()), ('tabs', 'UCfakeMIG17A', 'UCfakeMIG17A', 2, now()),
		('channel', 'UCfakeMIG17A', 'UCfakeMIG17A', 2, now()), ('comments', 'fakeMIG17v', 'UCfakeMIG17A', 1, now())`); err != nil {
		t.Fatal(err)
	}
	sql, err := db.MigrationSQL(17)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT kind, priority FROM schedule ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var k string
		var p int
		if err := rows.Scan(&k, &p); err != nil {
			t.Fatal(err)
		}
		got[k] = p
	}
	for k, want := range map[string]int{"rss": 0, "tabs": 0, "channel": 2, "comments": 1} {
		if got[k] != want {
			t.Errorf("%s at P%d; want P%d", k, got[k], want)
		}
	}
}

// Migration 19 (D-052) moves the rows already queued by the rule the
// workers now follow: a video read once is a refresh (P2), unless live or
// about to start; a comment check is P1 while its video or post is under
// two days old, P2 after, and a backfilled one's P3 once it has run.
func TestRefreshesMoveToP2(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO videos (id, channel_id, kind, published_at, last_checked, live_status) VALUES
			('fakeM19read', 'UCfakeMIG19', 'video', now() - interval '5 days', now(), 'none'),
			('fakeM19unread', 'UCfakeMIG19', 'video', now() - interval '1 hour', NULL, NULL),
			('fakeM19live', 'UCfakeMIG19', 'live', now() - interval '1 hour', now(), 'live'),
			('fakeM19soon', 'UCfakeMIG19', 'live', NULL, now(), 'upcoming'),
			('fakeM19new', 'UCfakeMIG19', 'video', now() - interval '1 hour', now(), 'none'),
			('fakeM19back', 'UCfakeMIG19', 'video', now() - interval '90 days', now(), 'none'),
			('fakeM19back2', 'UCfakeMIG19', 'video', now() - interval '90 days', NULL, NULL)`,
		`INSERT INTO posts (id, channel_id, published_at) VALUES
			('UgkxFAKEM19old', 'UCfakeMIG19', now() - interval '5 days'), ('UgkxFAKEM19new', 'UCfakeMIG19', now() - interval '1 hour')`,
		`INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at, last_ok) VALUES
			('video', 'fakeM19read', 'UCfakeMIG19', 0, now(), now()),
			('video', 'fakeM19unread', 'UCfakeMIG19', 0, now(), NULL),
			('video', 'fakeM19live', 'UCfakeMIG19', 0, now(), now()),
			('video', 'fakeM19soon', 'UCfakeMIG19', 0, now(), now()),
			('video', 'fakeM19back', 'UCfakeMIG19', 3, now(), now()),
			('comments', 'fakeM19read', 'UCfakeMIG19', 1, now(), now()),
			('comments', 'fakeM19new', 'UCfakeMIG19', 1, now(), now()),
			('comments', 'fakeM19back', 'UCfakeMIG19', 3, now(), now()),
			('comments', 'fakeM19back2', 'UCfakeMIG19', 3, now(), NULL),
			('post_comments', 'UgkxFAKEM19old', 'UCfakeMIG19', 1, now(), now()),
			('post_comments', 'UgkxFAKEM19new', 'UCfakeMIG19', 1, now(), now()),
			('rss', 'UCfakeMIG19', 'UCfakeMIG19', 0, now(), now())`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	sql, err := db.MigrationSQL(19)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind, id string
		want     int
	}{
		{"video", "fakeM19read", 2}, {"video", "fakeM19unread", 0}, {"video", "fakeM19live", 0}, {"video", "fakeM19soon", 0},
		{"video", "fakeM19back", 2},
		{"comments", "fakeM19read", 2}, {"comments", "fakeM19new", 1}, {"comments", "fakeM19back", 2}, {"comments", "fakeM19back2", 3},
		{"post_comments", "UgkxFAKEM19old", 2}, {"post_comments", "UgkxFAKEM19new", 1},
		{"rss", "UCfakeMIG19", 0},
	} {
		var p int
		if err := pool.QueryRow(ctx, `SELECT priority FROM schedule WHERE kind = $1 AND entity_id = $2`, c.kind, c.id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		if p != c.want {
			t.Errorf("%s %s at P%d; want P%d", c.kind, c.id, p, c.want)
		}
	}
}
