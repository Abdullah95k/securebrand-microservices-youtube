package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/discovery"
)

// The commenter graph counts each author's comments per channel, and a run
// writes only the pairs that are new or changed: a second run with nothing
// new writes none.
func TestCommenterGraphWritesOnlyChanges(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	const ch, other = "UCfakeGRAPHchannel000001", "UCfakeGRAPHchannel000002"
	const a, b = "UCfakeGRAPHauthorA000001", "UCfakeGRAPHauthorB000001"
	for _, id := range []string{ch, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO channels (id, status) VALUES ($1, 'listed')`, id); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	add := func(id, channel, author string, seen time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO comments (id, parent_kind, parent_id, channel_id, author_id, text, text_hash, first_seen, last_seen)
			VALUES ($1, 'video', 'fakeGRAPHv1', $2, $3, 'synthetic', '\x00', $4, $4)`, id, channel, author, seen); err != nil {
			t.Fatal(err)
		}
	}
	add("fakeGc1", ch, a, at)
	add("fakeGc2", ch, a, at)
	add("fakeGc3", other, a, at)
	add("fakeGc4", ch, b, at)
	add("fakeGc5", ch, ch, at) // a channel on its own videos: not an edge

	n, err := discovery.CommenterGraph(ctx, pool)
	if err != nil || n != 3 {
		t.Fatalf("first run wrote %d pairs (%v), want 3", n, err)
	}
	var w int
	if err := pool.QueryRow(ctx, `SELECT weight FROM channel_links_graph WHERE from_channel = $1 AND to_channel = $2 AND relation = 'commenter'`,
		a, ch).Scan(&w); err != nil || w != 2 {
		t.Fatalf("weight %d (%v), want 2", w, err)
	}
	if n, err := discovery.CommenterGraph(ctx, pool); err != nil || n != 0 {
		t.Fatalf("nothing new: wrote %d pairs (%v), want 0", n, err)
	}
	// A new comment changes one pair; a re-seen comment (a later scan)
	// moves another's last sighting.
	add("fakeGc6", ch, a, at.Add(time.Hour))
	if _, err := pool.Exec(ctx, `UPDATE comments SET last_seen = $2 WHERE id = $1`, "fakeGc4", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := discovery.CommenterGraph(ctx, pool); err != nil || n != 2 {
		t.Fatalf("after one new and one re-seen comment: wrote %d pairs (%v), want 2", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT weight FROM channel_links_graph WHERE from_channel = $1 AND to_channel = $2`, a, ch).Scan(&w); err != nil || w != 3 {
		t.Fatalf("weight %d (%v), want 3", w, err)
	}
}
