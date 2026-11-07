// Package outbox writes yts's events into the transactional outbox
// (REQ-010 fix 12, migration 0012), in the same transaction as the data
// they describe, for the relay to publish to Kafka.
//
// Two kinds of event, as the contract wants them:
//
//   - content: a channel, video, community post, comment, playlist or
//     commenter as stored after the write, whenever its content changed
//     (counts and bookkeeping left out of the comparison). The first write
//     of anything is a change. A removal or deletion is a change too.
//   - counts (metrics and channel snapshots): when any count read changed
//     since the last counts event, and at least daily for every item still
//     read (the contract's volume rule).
//
// What was last emitted per entity is in outbox_state. Every event carries
// when it was read (the contract's version) and its provenance: the job,
// the operations and raw answers read since the previous write, the exit
// country, and the collector's and parser's versions.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/provenance"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/version"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// Event kinds (event_outbox.kind). The relay maps each to its yt.* topic
// and its content.* message.
const (
	KindChannel         = "channel"          // content.account, yt.channel
	KindChannelSnapshot = "channel_snapshot" // content.account_snapshot, yt.channel_snapshot
	KindVideo           = "video"            // content.post, yt.video
	KindVideoMetrics    = "video_metrics"    // content.metrics, yt.video_metrics
	KindPost            = "post"             // content.post (community), yt.post
	KindPostMetrics     = "post_metrics"     // content.metrics, yt.post_metrics
	KindComment         = "comment"          // content.comment, yt.comment
	KindCommentMetrics  = "comment_metrics"  // content.metrics (target comment)
	KindPlaylist        = "playlist"         // content.playlist, yt.playlist
	KindAuthor          = "author"           // content.account (not monitored), yt.author_profile
	KindCandidate       = "candidate"        // discovery.candidate
	KindCoverage        = "coverage"         // collection.coverage (an hourly roll-up per job kind)
	KindMedia           = "media"            // content.media (the record, kept forever), yt.media
	KindMediaReady      = "media_ready"      // content.media.ready (a file announced to analysis)
	KindMediaDeleted    = "media_deleted"    // content.media.deleted (a file removed from storage)
)

// Table is how one stored table becomes events. Its SQL reads the row as t;
// $2 is the time of the observation (the snapshot tables' taken_at).
type Table struct {
	name string // the table
	id   string // its key column ("id" when empty)
	kind string // its content events' kind, and its outbox_state kind
	// SQL: the Kafka key's YouTube ID (the post, for comments).
	key string
	// SQL (jsonb): the content event's payload.
	payload string
	// SQL (jsonb): what decides that the content changed.
	compare string
	// Counts events: their kind ("" for none), and their payload (jsonb):
	// "counts" (the stored counts, which a read that missed one keeps) and
	// "observed" (this read's snapshot). A change is a change of "counts",
	// or of the live viewers read, so a read that missed a count, or a
	// removal, is not one.
	metricsKind string
	metrics     string

	contentSQL, metricsSQL string
}

// Kind is the table's content event kind.
func (t Table) Kind() string { return t.kind }

// MetricsKind is the table's counts event kind ("" when it has none).
func (t Table) MetricsKind() string { return t.metricsKind }

// The stored tables, as the writer, the comment engine and enrichment
// write them.
var (
	Channels = build(Table{
		name: "channels", kind: KindChannel, key: "t.id",
		payload: "to_jsonb(t)",
		compare: `to_jsonb(t) - ARRAY['subscribers_min', 'subscribers_text', 'videos_min', 'views_min',
			'last_checked', 'missed_checks', 'backfill_until', 'backfill_done_at']`,
		metricsKind: KindChannelSnapshot,
		metrics: `jsonb_build_object(
			'counts', jsonb_build_object('subscribers_min', t.subscribers_min, 'subscribers_text', t.subscribers_text,
				'videos_min', t.videos_min, 'views_min', t.views_min),
			'observed', (SELECT to_jsonb(s) - 'channel_id' FROM channel_snapshots s WHERE s.channel_id = t.id AND s.taken_at = $2),
			'handle', t.handle, 'title', t.title, 'status', t.status, 'removed_at', t.removed_at)`,
	})
	Videos = build(Table{
		name: "videos", kind: KindVideo, key: "t.id",
		payload: "to_jsonb(t)",
		compare: `to_jsonb(t) - ARRAY['views', 'likes_min', 'likes_text', 'comments_min', 'comments_text',
			'last_checked', 'missed_checks', 'comments_complete_pct']`,
		metricsKind: KindVideoMetrics,
		metrics: `jsonb_build_object(
			'counts', jsonb_build_object('views', t.views, 'likes_min', t.likes_min, 'likes_text', t.likes_text,
				'comments_min', t.comments_min, 'comments_text', t.comments_text),
			'observed', (SELECT to_jsonb(s) - 'video_id' FROM video_snapshots s WHERE s.video_id = t.id AND s.taken_at = $2),
			'channel_id', t.channel_id, 'live_status', t.live_status)`,
	})
	Posts = build(Table{
		name: "posts", kind: KindPost, key: "t.id",
		payload: "to_jsonb(t)",
		compare: `to_jsonb(t) - ARRAY['likes_min', 'likes_text', 'comments_min', 'comments_text',
			'last_checked', 'comments_read_at', 'detail_params']`,
		metricsKind: KindPostMetrics,
		metrics: `jsonb_build_object(
			'counts', jsonb_build_object('likes_min', t.likes_min, 'likes_text', t.likes_text,
				'comments_min', t.comments_min, 'comments_text', t.comments_text),
			'observed', (SELECT to_jsonb(s) - 'post_id' FROM post_snapshots s WHERE s.post_id = t.id AND s.taken_at = $2),
			'channel_id', t.channel_id)`,
	})
	Comments = build(Table{
		name: "comments", kind: KindComment, key: "t.parent_id",
		payload: "to_jsonb(t) - 'text_hash'",
		compare: `to_jsonb(t) - ARRAY['likes_min', 'likes_text', 'reply_count',
			'text_hash', 'last_seen', 'missed_sweeps']`,
		metricsKind: KindCommentMetrics,
		metrics: `jsonb_build_object(
			'counts', jsonb_build_object('likes_min', t.likes_min, 'likes_text', t.likes_text, 'reply_count', t.reply_count),
			'parent_kind', t.parent_kind, 'parent_id', t.parent_id, 'thread_id', t.thread_id, 'channel_id', t.channel_id)`,
	})
	// A playlist's event carries its items, removed ones too (with when).
	Playlists = build(Table{
		name: "playlists", kind: KindPlaylist, key: "t.id",
		payload: `to_jsonb(t) || jsonb_build_object('items', COALESCE((SELECT jsonb_agg(to_jsonb(i) - 'playlist_id'
			ORDER BY i.position, i.video_id) FROM playlist_items i WHERE i.playlist_id = t.id), '[]'::jsonb))`,
		compare: `(to_jsonb(t) - 'last_checked') || jsonb_build_object('items', COALESCE((SELECT jsonb_agg(
			to_jsonb(i) - ARRAY['playlist_id', 'last_seen'] ORDER BY i.position, i.video_id)
			FROM playlist_items i WHERE i.playlist_id = t.id), '[]'::jsonb))`,
	})
	// A commenter's event carries their latest About-panel profile, once
	// enriched, and whether they are a listed channel too (whose own events
	// then describe the account).
	Authors = build(Table{
		name: "authors", kind: KindAuthor, key: "t.id",
		payload: `to_jsonb(t) || jsonb_build_object('profile', (SELECT to_jsonb(p) - 'author_id' FROM author_profiles p
			WHERE p.author_id = t.id ORDER BY p.fetched_at DESC LIMIT 1),
			'monitored', EXISTS (SELECT 1 FROM channels c WHERE c.id = t.id AND c.status = 'listed'))`,
		compare: `(to_jsonb(t) - ARRAY['last_seen', 'comments_seen', 'relevant_comments', 'enriched_at'])
			|| jsonb_build_object('profile', (SELECT to_jsonb(p) - ARRAY['author_id', 'fetched_at', 'subscribers_min', 'videos_min']
			FROM author_profiles p WHERE p.author_id = t.id ORDER BY p.fetched_at DESC LIMIT 1))`,
	})
)

// Candidates are the channels discovery found. An event goes out when one
// is found, gains a source, or is decided; more sightings alone are not a
// change. It carries the latest evidence: the keywords and, for the
// website's search, the videos that matched. Official-API hits give their
// keyword only, never the API's video IDs or fields (the YouTube terms).
var Candidates = build(Table{
	name: "candidates", kind: KindCandidate, key: "t.channel_id", id: "channel_id",
	payload: `to_jsonb(t) || jsonb_build_object(
		'channel', (SELECT jsonb_build_object('kind', c.kind, 'status', c.status) FROM channels c WHERE c.id = t.channel_id),
		'evidence', COALESCE((SELECT jsonb_agg(e ORDER BY e.found_at DESC) FROM (
			SELECT h.engine, k.label AS keyword, h.found_at,
				CASE WHEN h.engine = 'web' THEN h.video_id END AS video_id,
				CASE WHEN h.engine = 'web' THEN h.published_at END AS published_at
			FROM search_hits h LEFT JOIN keywords k ON k.id = h.keyword_id
			WHERE h.channel_id = t.channel_id ORDER BY h.found_at DESC LIMIT 20) e), '[]'::jsonb))`,
	compare: `to_jsonb(t) - ARRAY['last_found', 'hits']`,
})

// build writes a table's two statements. $1: the IDs; $2: the observation
// time; $3: the provenance.
func build(t Table) Table {
	if t.id == "" {
		t.id = "id"
	}
	t.contentSQL = fmt.Sprintf(`WITH cur AS (
			SELECT t.%[6]s AS id, %[3]s AS key, %[4]s AS payload, sha256(convert_to((%[5]s)::text, 'UTF8')) AS h
			FROM %[1]s t WHERE t.%[6]s = ANY($1::text[])
		), changed AS (
			INSERT INTO outbox_state AS s (kind, entity_id, content_hash)
			SELECT '%[2]s', id, h FROM cur
			ON CONFLICT (kind, entity_id) DO UPDATE SET content_hash = EXCLUDED.content_hash
				WHERE s.content_hash IS DISTINCT FROM EXCLUDED.content_hash
			RETURNING entity_id
		)
		INSERT INTO event_outbox (kind, entity_id, partition_key, observed_at, payload, provenance)
		SELECT '%[2]s', cur.id, cur.key, $2::timestamptz, cur.payload, $3::jsonb
		FROM cur JOIN changed ON changed.entity_id = cur.id ORDER BY cur.id`,
		t.name, t.kind, t.key, t.payload, t.compare, t.id)
	if t.metricsKind != "" {
		t.metricsSQL = fmt.Sprintf(`WITH cur AS (
				SELECT t.id, %[4]s AS key, %[5]s AS payload FROM %[1]s t WHERE t.id = ANY($1::text[])
			), h AS (
				SELECT id, key, payload, sha256(convert_to(jsonb_build_object('counts', payload->'counts',
					'live_viewers', payload #> '{observed,live_viewers}')::text, 'UTF8')) AS mh FROM cur
			), changed AS (
				INSERT INTO outbox_state AS s (kind, entity_id, metrics_hash, metrics_at)
				SELECT '%[2]s', id, mh, $2::timestamptz FROM h
				ON CONFLICT (kind, entity_id) DO UPDATE SET metrics_hash = EXCLUDED.metrics_hash, metrics_at = EXCLUDED.metrics_at
					WHERE s.metrics_hash IS DISTINCT FROM EXCLUDED.metrics_hash OR s.metrics_at IS NULL
						OR s.metrics_at <= EXCLUDED.metrics_at - interval '1 day'
				RETURNING entity_id
			)
			INSERT INTO event_outbox (kind, entity_id, partition_key, observed_at, payload, provenance)
			SELECT '%[3]s', h.id, h.key, $2::timestamptz, h.payload, $3::jsonb
			FROM h JOIN changed ON changed.entity_id = h.id ORDER BY h.id`,
			t.name, t.kind, t.metricsKind, t.key, t.metrics)
	}
	return t
}

// Events adds one write's events. Make one per transaction with New: it
// takes the provenance read since the previous write.
type Events struct {
	at   time.Time
	prov []byte
}

// Provenance is an event's provenance (event_outbox.provenance).
type Provenance struct {
	provenance.Source
	Collector        string `json:"collector"`
	CollectorVersion string `json:"collector_version"`
	CollectorRev     string `json:"collector_revision,omitempty"`
	ParserVersion    string `json:"parser_version"`
}

// New starts a write observed at at, with the provenance in ctx.
func New(ctx context.Context, at time.Time) *Events {
	p := Provenance{Source: provenance.From(ctx).Take(), Collector: "yts",
		CollectorVersion: version.Version, CollectorRev: version.Revision, ParserVersion: ytparse.Version}
	b, err := json.Marshal(p)
	if err != nil {
		b = []byte("{}")
	}
	return &Events{at: at, prov: b}
}

// At is the observation time.
func (e *Events) At() time.Time { return e.at }

// Content adds a content event for each of ids whose stored content
// changed since its last one. It returns how many it added.
func (e *Events) Content(ctx context.Context, q db.Querier, t Table, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := q.Exec(ctx, t.contentSQL, ids, e.at, e.prov)
	if err != nil {
		return 0, fmt.Errorf("outbox: %s events: %w", t.kind, err)
	}
	return tag.RowsAffected(), nil
}

// Metrics adds a counts event for each of ids whose counts changed since
// the last one, or whose last one went out a day or more before. It
// returns how many it added.
func (e *Events) Metrics(ctx context.Context, q db.Querier, t Table, ids ...string) (int64, error) {
	if len(ids) == 0 || t.metricsSQL == "" {
		return 0, nil
	}
	tag, err := q.Exec(ctx, t.metricsSQL, ids, e.at, e.prov)
	if err != nil {
		return 0, fmt.Errorf("outbox: %s events: %w", t.metricsKind, err)
	}
	return tag.RowsAffected(), nil
}

// Write adds both: content events, then counts events.
func (e *Events) Write(ctx context.Context, q db.Querier, t Table, ids ...string) error {
	if _, err := e.Content(ctx, q, t, ids...); err != nil {
		return err
	}
	_, err := e.Metrics(ctx, q, t, ids...)
	return err
}

// Coverage adds an hourly roll-up event per job kind (the contract's
// JobCoverage, job_count jobs) for each hour of usage_hourly (fix 5) that
// started at or after from and ended by to, once each. It returns how many
// it added.
func (e *Events) Coverage(ctx context.Context, q db.Querier, from, to time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `WITH cur AS (
			SELECT u.kind || '@' || to_char(u.hour AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24":00Z"') AS id, to_jsonb(u) AS payload
			FROM usage_hourly u WHERE u.hour >= $1 AND u.hour + interval '1 hour' <= $2
		), fresh AS (
			INSERT INTO outbox_state (kind, entity_id, content_hash) SELECT 'coverage', id, '\x00'::bytea FROM cur
			ON CONFLICT (kind, entity_id) DO NOTHING RETURNING entity_id
		)
		INSERT INTO event_outbox (kind, entity_id, partition_key, observed_at, payload, provenance)
		SELECT 'coverage', cur.id, cur.id, $3::timestamptz, cur.payload, $4::jsonb
		FROM cur JOIN fresh ON fresh.entity_id = cur.id ORDER BY cur.id`, from, to, e.at, e.prov)
	if err != nil {
		return 0, fmt.Errorf("outbox: coverage events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MediaKey names one stored image (the media table's key).
type MediaKey struct{ Kind, EntityID, SHA256 string }

// ID is the image's media ID: kind, owner and the first 16 hex of its
// SHA-256 ("thumb:<video>:<hash>"); the contract's is "yt:media:" and this.
func (k MediaKey) ID() string {
	h := k.SHA256
	if len(h) > 16 {
		h = h[:16]
	}
	return k.Kind + ":" + k.EntityID + ":" + h
}

// MediaEvent adds an event of kind (KindMedia, KindMediaReady or
// KindMediaDeleted) for each image, from its row as it is now, keyed by
// its media ID.
func (e *Events) MediaEvent(ctx context.Context, q db.Querier, kind string, keys ...MediaKey) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	ks, es, hs := make([]string, len(keys)), make([]string, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		ks[i], es[i], hs[i] = k.Kind, k.EntityID, k.SHA256
	}
	tag, err := q.Exec(ctx, `INSERT INTO event_outbox (kind, entity_id, partition_key, observed_at, payload, provenance)
		SELECT $1, m.kind || ':' || m.entity_id || ':' || left(m.sha256, 16), m.kind || ':' || m.entity_id || ':' || left(m.sha256, 16),
			$5::timestamptz, to_jsonb(m), $6::jsonb
		FROM media m JOIN unnest($2::text[], $3::text[], $4::text[]) AS k (kind, entity_id, sha256) USING (kind, entity_id, sha256)
		ORDER BY m.kind, m.entity_id, m.sha256`, kind, ks, es, hs, e.at, e.prov)
	if err != nil {
		return 0, fmt.Errorf("outbox: %s events: %w", kind, err)
	}
	return tag.RowsAffected(), nil
}

// Keep is how long sent events stay (the owner, 7 November): they are
// copies made for sending, and the data stays in its tables.
const Keep = 7 * 24 * time.Hour

// Prune deletes events sent before cutoff, a batch at a time, and returns
// how many. Unsent events are never deleted.
func Prune(ctx context.Context, q db.Querier, cutoff time.Time) (int64, error) {
	const batch = 10000
	var total int64
	for {
		tag, err := q.Exec(ctx, `DELETE FROM event_outbox WHERE id IN (
			SELECT id FROM event_outbox WHERE published_at < $1 ORDER BY published_at LIMIT $2)`, cutoff, batch)
		if err != nil {
			return total, fmt.Errorf("outbox: pruning sent events: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < batch || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}

// Row is one outbox event as stored.
type Row struct {
	ID           int64
	Kind         string
	EntityID     string
	PartitionKey string
	ObservedAt   time.Time
	Payload      []byte // JSON
	Provenance   []byte // JSON: Provenance
}

// Unsent returns up to limit events not yet sent, after id after, oldest
// first.
func Unsent(ctx context.Context, q db.Querier, after int64, limit int) ([]Row, error) {
	rows, err := q.Query(ctx, `SELECT id, kind, entity_id, partition_key, observed_at, payload, provenance
		FROM event_outbox WHERE published_at IS NULL AND id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox: reading unsent events: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.Kind, &r.EntityID, &r.PartitionKey, &r.ObservedAt, &r.Payload, &r.Provenance); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Pending returns up to limit events to send, oldest first: not sent, and
// not refused (failure set) since the relay started.
func Pending(ctx context.Context, q db.Querier, limit int) ([]Row, error) {
	rows, err := q.Query(ctx, `SELECT id, kind, entity_id, partition_key, observed_at, payload, provenance
		FROM event_outbox WHERE published_at IS NULL AND failure IS NULL ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox: reading pending events: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.Kind, &r.EntityID, &r.PartitionKey, &r.ObservedAt, &r.Payload, &r.Provenance); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkSent records that the events were published, at now.
func MarkSent(ctx context.Context, q db.Querier, ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `UPDATE event_outbox SET published_at = $2, attempts = attempts + 1, failure = NULL
		WHERE id = ANY($1)`, ids, now)
	return err
}

// MarkFailed records why an event can't be published (the contract
// refused it, or Kafka can't take it). It stays unsent, and is tried again
// when the relay restarts (after a fix).
func MarkFailed(ctx context.Context, q db.Querier, id int64, reason string) error {
	if len(reason) > 2000 {
		reason = reason[:2000]
	}
	_, err := q.Exec(ctx, `UPDATE event_outbox SET failure = $2, attempts = attempts + 1 WHERE id = $1`, id, reason)
	return err
}

// RetryFailed clears the failures of unsent events, so they are tried
// again; the relay calls it when it starts. It returns how many.
func RetryFailed(ctx context.Context, q db.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE event_outbox SET failure = NULL WHERE published_at IS NULL AND failure IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
