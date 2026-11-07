package outbox_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/provenance"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

const (
	chID    = "UCfakeOUTBOX000000000001"
	videoID = "fakeVideo01"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

type event struct {
	kind, entity, key string
	at                time.Time
	payload, prov     map[string]any
}

func events(t *testing.T, pool *pgxpool.Pool) []event {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT kind, entity_id, partition_key, observed_at, payload, provenance
		FROM event_outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		var p, pr []byte
		if err := rows.Scan(&e.kind, &e.entity, &e.key, &e.at, &p, &pr); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(p, &e.payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(pr, &e.prov); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// write adds the content and counts events of one observation at at.
func write(t *testing.T, pool *pgxpool.Pool, at time.Time, tb outbox.Table, ids ...string) (content, metrics int64) {
	t.Helper()
	ctx := context.Background()
	ev := outbox.New(ctx, at)
	content, err := ev.Content(ctx, pool, tb, ids...)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err = ev.Metrics(ctx, pool, tb, ids...)
	if err != nil {
		t.Fatal(err)
	}
	return content, metrics
}

func snapshot(t *testing.T, pool *pgxpool.Pool, at time.Time, views int64) {
	t.Helper()
	exec(t, pool, `UPDATE videos SET views = $2, last_checked = $3 WHERE id = $1`, videoID, views, at)
	exec(t, pool, `INSERT INTO video_snapshots (video_id, taken_at, views) VALUES ($1, $2, $3)`, videoID, at, views)
}

// A video's content event goes out when its content changes, never for
// counts or bookkeeping; its counts go out when they change, and daily.
func TestVideoEventsOnChangeOnly(t *testing.T) {
	pool := dbtest.New(t)
	exec(t, pool, `INSERT INTO videos (id, channel_id, kind, title, description, live_status) VALUES ($1, $2, 'video', 'A title', 'Words', 'none')`,
		videoID, chID)
	snapshot(t, pool, t0, 100)

	if c, m := write(t, pool, t0, outbox.Videos, videoID); c != 1 || m != 1 {
		t.Fatalf("first read: %d content and %d counts events, want 1 and 1", c, m)
	}
	// A later read with the same views and only bookkeeping changed.
	at := t0.Add(time.Hour)
	snapshot(t, pool, at, 100)
	exec(t, pool, `UPDATE videos SET missed_checks = 0, comments_complete_pct = 50 WHERE id = $1`, videoID)
	if c, m := write(t, pool, at, outbox.Videos, videoID); c != 0 || m != 0 {
		t.Fatalf("nothing changed: %d content and %d counts events, want none", c, m)
	}
	// More views: counts only.
	at = t0.Add(2 * time.Hour)
	snapshot(t, pool, at, 150)
	if c, m := write(t, pool, at, outbox.Videos, videoID); c != 0 || m != 1 {
		t.Fatalf("views changed: %d content and %d counts events, want 0 and 1", c, m)
	}
	// An edited title: content only.
	at = t0.Add(3 * time.Hour)
	snapshot(t, pool, at, 150)
	exec(t, pool, `UPDATE videos SET title = 'A new title' WHERE id = $1`, videoID)
	if c, m := write(t, pool, at, outbox.Videos, videoID); c != 1 || m != 0 {
		t.Fatalf("title changed: %d content and %d counts events, want 1 and 0", c, m)
	}
	// The same counts a day after the last counts event: sent again.
	at = t0.Add(26 * time.Hour)
	snapshot(t, pool, at, 150)
	if c, m := write(t, pool, at, outbox.Videos, videoID); c != 0 || m != 1 {
		t.Fatalf("a day later: %d content and %d counts events, want 0 and 1", c, m)
	}
	// Removed: content.
	at = t0.Add(27 * time.Hour)
	exec(t, pool, `UPDATE videos SET removed_at = $2 WHERE id = $1`, videoID, at)
	if c, m := write(t, pool, at, outbox.Videos, videoID); c != 1 || m != 0 {
		t.Fatalf("removed: %d content and %d counts events, want 1 and 0", c, m)
	}
	// Live viewers are counts too.
	at = t0.Add(28 * time.Hour)
	exec(t, pool, `INSERT INTO video_snapshots (video_id, taken_at, views, live_viewers) VALUES ($1, $2, 150, 40)`, videoID, at)
	if c, m := write(t, pool, at, outbox.Videos, videoID); c != 0 || m != 1 {
		t.Fatalf("live viewers: %d content and %d counts events, want 0 and 1", c, m)
	}

	ev := events(t, pool)
	kinds := []string{"video", "video_metrics", "video_metrics", "video", "video_metrics", "video", "video_metrics"}
	if len(ev) != len(kinds) {
		t.Fatalf("%d events, want %d", len(ev), len(kinds))
	}
	for i, e := range ev {
		if e.kind != kinds[i] || e.entity != videoID || e.key != videoID {
			t.Errorf("event %d: %s %s key %s, want %s %s key %s", i, e.kind, e.entity, e.key, kinds[i], videoID, videoID)
		}
	}
	if ev[0].payload["title"] != "A title" || ev[3].payload["title"] != "A new title" || ev[5].payload["removed_at"] == nil {
		t.Errorf("content payloads: %v / %v / %v", ev[0].payload["title"], ev[3].payload["title"], ev[5].payload["removed_at"])
	}
	if !ev[0].at.Equal(t0) || !ev[3].at.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("observed at %v and %v", ev[0].at, ev[3].at)
	}
	counts := ev[2].payload["counts"].(map[string]any)
	observed := ev[2].payload["observed"].(map[string]any)
	if counts["views"] != float64(150) || observed["views"] != float64(150) || ev[2].payload["channel_id"] != chID {
		t.Errorf("counts payload: %v", ev[2].payload)
	}
}

// A comment's events are keyed by its post, and a known comment seen again
// unchanged adds nothing.
func TestCommentEventsKeyedByPost(t *testing.T) {
	pool := dbtest.New(t)
	for _, id := range []string{"UgzFakeComment1", "UgzFakeComment2"} {
		exec(t, pool, `INSERT INTO comments (id, parent_kind, parent_id, channel_id, author_id, text, text_hash, likes_min, first_seen, last_seen)
			VALUES ($1, 'video', $2, $3, 'UCfakeAUTHOR0000000000001', 'Nice video', '\x00', 3, $4, $4)`, id, videoID, chID, t0)
	}
	ids := []string{"UgzFakeComment1", "UgzFakeComment2"}
	if c, m := write(t, pool, t0, outbox.Comments, ids...); c != 2 || m != 2 {
		t.Fatalf("new comments: %d content and %d counts events, want 2 and 2", c, m)
	}
	at := t0.Add(time.Hour)
	exec(t, pool, `UPDATE comments SET last_seen = $1`, at)
	if c, m := write(t, pool, at, outbox.Comments, ids...); c != 0 || m != 0 {
		t.Fatalf("seen again: %d content and %d counts events, want none", c, m)
	}
	exec(t, pool, `UPDATE comments SET text = 'Nice video!', version = 2, likes_min = 4 WHERE id = 'UgzFakeComment2'`)
	if c, m := write(t, pool, at, outbox.Comments, ids...); c != 1 || m != 1 {
		t.Fatalf("one edited: %d content and %d counts events, want 1 and 1", c, m)
	}
	for _, e := range events(t, pool) {
		if e.key != videoID {
			t.Errorf("%s %s keyed %s, want the video %s", e.kind, e.entity, e.key, videoID)
		}
		if _, ok := e.payload["text_hash"]; ok {
			t.Errorf("%s payload carries text_hash", e.kind)
		}
	}
}

// A playlist's event carries its items, and a changed list is a change.
func TestPlaylistEventsCarryItems(t *testing.T) {
	pool := dbtest.New(t)
	const pl = "PLfakeOUTBOX0001"
	exec(t, pool, `INSERT INTO playlists (id, channel_id, title, first_seen, last_checked) VALUES ($1, $2, 'Episodes', $3, $3)`, pl, chID, t0)
	exec(t, pool, `INSERT INTO playlist_items (playlist_id, video_id, position, first_seen, last_seen) VALUES ($1, 'fakeVideo01', 1, $2, $2)`, pl, t0)
	if c, _ := write(t, pool, t0, outbox.Playlists, pl); c != 1 {
		t.Fatalf("first read: %d events", c)
	}
	at := t0.Add(time.Hour)
	exec(t, pool, `UPDATE playlist_items SET last_seen = $1`, at)
	exec(t, pool, `UPDATE playlists SET last_checked = $1`, at)
	if c, _ := write(t, pool, at, outbox.Playlists, pl); c != 0 {
		t.Fatalf("read again unchanged: %d events", c)
	}
	exec(t, pool, `INSERT INTO playlist_items (playlist_id, video_id, position, first_seen, last_seen) VALUES ($1, 'fakeVideo02', 2, $2, $2)`, pl, at)
	if c, _ := write(t, pool, at, outbox.Playlists, pl); c != 1 {
		t.Fatalf("a video added: %d events", c)
	}
	ev := events(t, pool)
	if items := ev[1].payload["items"].([]any); len(items) != 2 || items[1].(map[string]any)["video_id"] != "fakeVideo02" {
		t.Errorf("items: %v", ev[1].payload["items"])
	}
}

// A commenter's event carries the latest profile; their comment counters
// and enrichment times are not changes.
func TestAuthorEventsCarryProfile(t *testing.T) {
	pool := dbtest.New(t)
	const a = "UCfakeAUTHOR0000000000001"
	exec(t, pool, `INSERT INTO authors (id, handle, first_seen, last_seen, comments_seen) VALUES ($1, '@fakeauthor', $2, $2, 1)`, a, t0)
	if c, m := write(t, pool, t0, outbox.Authors, a); c != 1 || m != 0 {
		t.Fatalf("new commenter: %d content and %d counts events, want 1 and 0", c, m)
	}
	exec(t, pool, `UPDATE authors SET comments_seen = 5, last_seen = $2 WHERE id = $1`, a, t0.Add(time.Hour))
	if c, _ := write(t, pool, t0.Add(time.Hour), outbox.Authors, a); c != 0 {
		t.Fatalf("more comments: %d events, want none", c)
	}
	exec(t, pool, `INSERT INTO author_profiles (author_id, fetched_at, description, subscribers_min) VALUES ($1, $2, 'About me', 10)`, a, t0.Add(2*time.Hour))
	exec(t, pool, `UPDATE authors SET enriched_at = $2 WHERE id = $1`, a, t0.Add(2*time.Hour))
	if c, _ := write(t, pool, t0.Add(2*time.Hour), outbox.Authors, a); c != 1 {
		t.Fatalf("enriched: %d events, want 1", c)
	}
	// Enriched again: only the subscriber count moved.
	exec(t, pool, `INSERT INTO author_profiles (author_id, fetched_at, description, subscribers_min) VALUES ($1, $2, 'About me', 12)`, a, t0.Add(3*time.Hour))
	if c, _ := write(t, pool, t0.Add(3*time.Hour), outbox.Authors, a); c != 0 {
		t.Fatalf("re-enriched, same profile: %d events, want none", c)
	}
	ev := events(t, pool)
	if p, _ := ev[1].payload["profile"].(map[string]any); p["description"] != "About me" {
		t.Errorf("profile: %v", ev[1].payload["profile"])
	}
}

// A channel's snapshot event carries the counts and the snapshot read.
func TestChannelSnapshotEvent(t *testing.T) {
	pool := dbtest.New(t)
	exec(t, pool, `INSERT INTO channels (id, handle, title, subscribers_min, subscribers_text, kind, tier, status)
		VALUES ($1, '@fakechannel', 'Fake', 1200, '1.2K subscribers', 'core', 'B', 'listed')`, chID)
	exec(t, pool, `INSERT INTO channel_snapshots (channel_id, taken_at, subscribers_min, subscribers_text) VALUES ($1, $2, 1200, '1.2K subscribers')`, chID, t0)
	if c, m := write(t, pool, t0, outbox.Channels, chID); c != 1 || m != 1 {
		t.Fatalf("first read: %d content and %d counts events", c, m)
	}
	ev := events(t, pool)
	if ev[1].kind != outbox.KindChannelSnapshot || ev[1].payload["observed"].(map[string]any)["subscribers_text"] != "1.2K subscribers" {
		t.Errorf("snapshot event: %s %v", ev[1].kind, ev[1].payload)
	}
}

// Every event carries where its data came from: the job, the operations
// and raw answers read since the last write, the exit country, and the
// versions.
func TestProvenance(t *testing.T) {
	pool := dbtest.New(t)
	exec(t, pool, `INSERT INTO videos (id, channel_id, kind, live_status) VALUES ($1, $2, 'video', 'none')`, videoID, chID)
	ctx, trail := provenance.With(context.Background(), provenance.Job{ID: "video/fakeVideo01/1", Kind: "video"})
	trail.Record("next", "s3://raw/yts/30d/a.json.gz", "IQ")
	trail.Record("player", "", "IQ")
	ev := outbox.New(ctx, t0)
	if _, err := ev.Content(ctx, pool, outbox.Videos, videoID); err != nil {
		t.Fatal(err)
	}
	p := events(t, pool)[0].prov
	want := map[string]any{"job_id": "video/fakeVideo01/1", "job_kind": "video", "collector": "yts", "parser_version": ytparse.Version}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("provenance %s = %v, want %v", k, p[k], v)
		}
	}
	if ops := p["operations"].([]any); len(ops) != 2 || ops[0] != "next" || ops[1] != "player" {
		t.Errorf("operations: %v", p["operations"])
	}
	if refs := p["raw_refs"].([]any); len(refs) != 1 {
		t.Errorf("raw refs: %v", p["raw_refs"])
	}
	if c := p["exit_countries"].([]any); len(c) != 1 || c[0] != "IQ" {
		t.Errorf("exit countries: %v", p["exit_countries"])
	}
	// The next write, with nothing read since, came from the same answers.
	if s := trail.Take(); len(s.Operations) != 2 || s.JobID != "video/fakeVideo01/1" {
		t.Errorf("a second write from the same answers: %+v", s)
	}
}

// Sent events go after 7 days; unsent ones never.
func TestPrune(t *testing.T) {
	pool := dbtest.New(t)
	for i, sent := range []any{nil, t0.Add(-8 * 24 * time.Hour), t0.Add(-6 * 24 * time.Hour)} {
		exec(t, pool, `INSERT INTO event_outbox (kind, entity_id, partition_key, observed_at, payload, published_at)
			VALUES ('video', $1, $1, $2, '{}', $3)`, []string{"a", "b", "c"}[i], t0.Add(-9*24*time.Hour), sent)
	}
	n, err := outbox.Prune(context.Background(), pool, t0.Add(-outbox.Keep))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d (%v), want 1", n, err)
	}
	var left []string
	rows, _ := pool.Query(context.Background(), `SELECT entity_id FROM event_outbox ORDER BY entity_id`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		left = append(left, s)
	}
	rows.Close()
	if len(left) != 2 || left[0] != "a" || left[1] != "c" {
		t.Errorf("left %v, want the unsent one and the one sent 6 days ago", left)
	}
}

// A candidate's event goes out when it is found, gains a source or is
// decided; more sightings alone are not a change. An official-API hit
// gives its keyword only.
func TestCandidateEvents(t *testing.T) {
	pool := dbtest.New(t)
	const c = "UCfakeCANDIDATE000000001"
	exec(t, pool, `INSERT INTO keywords (label, kind) VALUES ('baghdad', 'place')`)
	exec(t, pool, `INSERT INTO search_hits (engine, keyword_id, video_id, channel_id, title, found_at)
		SELECT 'api', id, 'fakeAPIvideo', $1, 'an API title', $2 FROM keywords`, c, t0)
	exec(t, pool, `INSERT INTO candidates (channel_id, sources) VALUES ($1, ARRAY['api_search'])`, c)
	if n, _ := write(t, pool, t0, outbox.Candidates, c); n != 1 {
		t.Fatalf("found: %d events", n)
	}
	exec(t, pool, `UPDATE candidates SET hits = hits + 1, last_found = $2 WHERE channel_id = $1`, c, t0.Add(time.Hour))
	if n, _ := write(t, pool, t0.Add(time.Hour), outbox.Candidates, c); n != 0 {
		t.Fatalf("seen again: %d events, want none", n)
	}
	exec(t, pool, `INSERT INTO search_hits (engine, keyword_id, video_id, channel_id, found_at)
		SELECT 'web', id, 'fakeWEBvideo', $1, $2 FROM keywords`, c, t0.Add(2*time.Hour))
	exec(t, pool, `UPDATE candidates SET sources = array_append(sources, 'web_search') WHERE channel_id = $1`, c)
	if n, _ := write(t, pool, t0.Add(2*time.Hour), outbox.Candidates, c); n != 1 {
		t.Fatalf("a new source: %d events", n)
	}
	exec(t, pool, `UPDATE candidates SET status = 'no_match', checked_at = $2, decision = 'not relevant' WHERE channel_id = $1`, c, t0.Add(3*time.Hour))
	if n, _ := write(t, pool, t0.Add(3*time.Hour), outbox.Candidates, c); n != 1 {
		t.Fatalf("decided: %d events", n)
	}
	ev := events(t, pool)
	evidence := ev[1].payload["evidence"].([]any)
	if len(evidence) != 2 {
		t.Fatalf("evidence %v", evidence)
	}
	for _, e := range evidence {
		e := e.(map[string]any)
		if e["keyword"] != "baghdad" || (e["engine"] == "api") != (e["video_id"] == nil) {
			t.Errorf("evidence %v: the API's video must never be carried", e)
		}
		if _, ok := e["title"]; ok {
			t.Errorf("evidence %v carries a title", e)
		}
	}
	if ev[0].key != c {
		t.Errorf("keyed %q", ev[0].key)
	}
}

// One coverage roll-up per job kind and finished hour, once.
func TestCoverageOnce(t *testing.T) {
	pool := dbtest.New(t)
	h1, h2 := t0.Add(-2*time.Hour), t0.Add(-time.Hour)
	for _, h := range []time.Time{h1, h2, t0} {
		exec(t, pool, `INSERT INTO usage_hourly (hour, kind, jobs, failures, requests, bytes, blocks, run_ms) VALUES ($1, 'video', 10, 1, 30, 9000, 2, 5000)`, h)
	}
	ctx := context.Background()
	ev := outbox.New(ctx, t0.Add(15*time.Minute))
	n, err := ev.Coverage(ctx, pool, t0.Add(-48*time.Hour), t0.Add(5*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("roll-ups %d (%v), want the two finished hours", n, err)
	}
	if n, _ := ev.Coverage(ctx, pool, t0.Add(-48*time.Hour), t0.Add(5*time.Minute)); n != 0 {
		t.Errorf("again: %d, want none", n)
	}
	got := events(t, pool)
	if got[0].entity != "video@"+h1.Format("2006-01-02T15")+":00Z" || got[0].payload["jobs"] != float64(10) {
		t.Errorf("roll-up %s %v", got[0].entity, got[0].payload)
	}
}
