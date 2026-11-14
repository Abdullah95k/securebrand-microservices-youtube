// Package discovery finds Iraq-relevant channels that are not on the list
// yet (plan §5):
//
//   - the search runner searches every keyword spelling on the website on
//     rotation (priority keywords hourly, the rest every 6 hours), newest
//     first, and sweeps priority keywords on the official API every 3 hours
//     with an exact time window, under a hard daily quota ceiling kept in
//     the database;
//   - every hit (keyword → video → channel) is recorded; API hits are
//     deleted after 30 days (YouTube's API rule);
//   - a channel not on the list becomes a candidate; the candidate check
//     lists it when the channel itself posted at least one item matching
//     the keywords (D10, no size minimum), as core (country Iraq, ≥ 30% of
//     recent items matching, or its own page matching) or mention;
//   - no-match candidates are checked again after 90 days; erased channels
//     are never added back.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// DefaultQuotaCap is the daily ceiling of official API units this system
// may use (Google's default allowance is 10,000; 500 stay in reserve).
const DefaultQuotaCap = 9500

// DefaultWebParams sorts website search results by upload date, newest
// first. It can be changed in the settings (key "search_web_params").
const DefaultWebParams = "CAI%3D"

// Runner is the search runner and the candidate check.
type Runner struct {
	Pool     *pgxpool.Pool
	Fetch    *phase0.Collector
	API      *ytapi.Client // nil: website search only
	Write    *writer.Writer
	Sup      *privacy.Suppressor
	Log      *slog.Logger
	Now      func() time.Time
	QuotaCap int
	WebPages int // pages per website search (default 5)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Handlers maps the discovery job kinds to their handlers.
func (r *Runner) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{
		queue.KindSearch:    r.search,
		queue.KindCandidate: r.candidate,
	}
}

// Reserve takes units from today's official API quota (the quota day
// resets at midnight Pacific time). It returns false when the ceiling would
// be passed; the ceiling holds across every process, since it is checked
// in the database.
func Reserve(ctx context.Context, q db.Querier, now time.Time, units, ceiling int) (bool, error) {
	day := ytapi.PacificDay(now)
	if _, err := q.Exec(ctx, `INSERT INTO quota_ledger (day, units_used, units_cap) VALUES ($1, 0, $2) ON CONFLICT (day) DO NOTHING`,
		day, ceiling); err != nil {
		return false, err
	}
	tag, err := q.Exec(ctx, `UPDATE quota_ledger SET units_used = units_used + $2, updated_at = now()
		WHERE day = $1 AND units_used + $2 <= LEAST(units_cap, $3)`, day, units, ceiling)
	return tag.RowsAffected() == 1, err
}

// SyncTerms makes the search rotation match the keywords: a new keyword's
// spellings get search jobs at once, a removed one's jobs go. It returns
// how many terms were added and removed.
func SyncTerms(ctx context.Context, pool *pgxpool.Pool) (added, removed int, err error) {
	terms, err := kwstore.SearchTerms(ctx, pool)
	if err != nil {
		return 0, 0, err
	}
	keep := make([]string, 0, len(terms))
	for _, t := range terms {
		keep = append(keep, t.Normalized)
		tag, err := pool.Exec(ctx, `INSERT INTO search_terms (term, query, keyword_id) VALUES ($1, $2, $3)
			ON CONFLICT (term) DO UPDATE SET query = EXCLUDED.query, keyword_id = EXCLUDED.keyword_id`, t.Normalized, t.Term, t.KeywordID)
		if err != nil {
			return added, removed, err
		}
		_ = tag
		ok, err := queue.Ensure(ctx, pool, queue.KindSearch, t.Normalized, "", queue.P2, time.Now())
		if err != nil {
			return added, removed, err
		}
		if ok {
			added++
		}
	}
	tag, err := pool.Exec(ctx, `DELETE FROM search_terms WHERE NOT (term = ANY($1))`, keep)
	if err != nil {
		return added, removed, err
	}
	removed = int(tag.RowsAffected())
	_, err = pool.Exec(ctx, `DELETE FROM schedule WHERE kind = $1 AND NOT (entity_id = ANY($2))`, queue.KindSearch, keep)
	return added, removed, err
}

// APIDataMaxAge is how long data from the official Data API may be kept
// (its terms: refresh or delete within 30 days).
const APIDataMaxAge = 30 * 24 * time.Hour

// apiCheckDeadline is when an unchecked candidate found only by the API is
// moved to the front of the queue, so its check (which reads the channel by
// scraping) happens before APIDataMaxAge.
const apiCheckDeadline = 20 * 24 * time.Hour

// PurgeAPIHits deletes official-API hits older than 30 days.
func PurgeAPIHits(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM search_hits WHERE engine = 'api' AND found_at < $1`, now.Add(-APIDataMaxAge))
	return tag.RowsAffected(), err
}

// APIExpiry is what ExpireAPIData did.
type APIExpiry struct {
	Hits        int64 // official-API search hits deleted
	Videos      int64 // api_search videos whose API title and time were cleared
	Candidates  int64 // API-only candidates removed, unchecked after 30 days
	Prioritised int64 // API-only candidate checks moved to the front of the queue
}

// ExpireAPIData keeps the official Data API's 30-day rule for everything yts
// first learned from the API (REQ-010 fix 10; REQ-004 §1):
//
//   - search hits from the API are deleted after 30 days;
//   - a video first stored from an API hit (source api_search) starts with
//     the API's title and time, and its website read replaces them. If that
//     read never succeeded within 30 days (last_checked still empty), the
//     API's title and time are cleared; the row keeps the video ID, so a
//     later website read fills it;
//   - a candidate found only by the API is checked by reading its channel
//     page, after which its ID is scraped data. A check still waiting after
//     20 days is moved to the front of the queue; a candidate still
//     unchecked after 30 days is removed, with its job.
//
// Data read from the website is never touched.
func ExpireAPIData(ctx context.Context, q db.Querier, now time.Time) (APIExpiry, error) {
	var e APIExpiry
	var err error
	if e.Hits, err = PurgeAPIHits(ctx, q, now); err != nil {
		return e, err
	}
	cutoff := now.Add(-APIDataMaxAge)
	tag, err := q.Exec(ctx, `UPDATE videos SET title = NULL, published_at = NULL, published_precision_s = NULL
		WHERE source = 'api_search' AND last_checked IS NULL AND first_seen < $1
		  AND (title IS NOT NULL OR published_at IS NOT NULL)`, cutoff)
	if err != nil {
		return e, err
	}
	e.Videos = tag.RowsAffected()
	// One statement, so a candidate, its job and its unsent outbox copies
	// (fix 12) go together: nothing learned from the API outlives 30 days.
	err = q.QueryRow(ctx, `WITH gone AS (
			DELETE FROM candidates
			WHERE status = 'new' AND sources = ARRAY['api_search']::text[] AND first_found < $1
			RETURNING channel_id),
		jobs AS (DELETE FROM schedule WHERE kind = $2 AND entity_id IN (SELECT channel_id FROM gone) RETURNING 1),
		events AS (DELETE FROM event_outbox WHERE kind = 'candidate' AND entity_id IN (SELECT channel_id FROM gone) RETURNING 1),
		state AS (DELETE FROM outbox_state WHERE kind = 'candidate' AND entity_id IN (SELECT channel_id FROM gone) RETURNING 1)
		SELECT (SELECT count(*) FROM gone), (SELECT count(*) FROM jobs), (SELECT count(*) FROM events), (SELECT count(*) FROM state)`,
		cutoff, queue.KindCandidate).Scan(&e.Candidates, new(int64), new(int64), new(int64))
	if err != nil {
		return e, err
	}
	tag, err = q.Exec(ctx, `UPDATE schedule SET priority = 1, due_at = LEAST(due_at, now())
		WHERE kind = $1 AND priority > 1 AND entity_id IN (
			SELECT channel_id FROM candidates
			WHERE status = 'new' AND sources = ARRAY['api_search']::text[] AND first_found < $2)`,
		queue.KindCandidate, now.Add(-apiCheckDeadline))
	if err != nil {
		return e, err
	}
	e.Prioritised = tag.RowsAffected()
	return e, err
}

// hit is one search result.
type hit struct {
	videoID, channelID, title string
	published                 time.Time
}

func (r *Runner) search(ctx context.Context, j queue.Job) (time.Time, error) {
	var query string
	var keywordID int64
	var prio int
	var lastAPI, lastWeb *time.Time
	err := r.Pool.QueryRow(ctx, `SELECT t.query, t.keyword_id, k.priority, t.last_api, t.last_web
		FROM search_terms t JOIN keywords k ON k.id = t.keyword_id WHERE t.term = $1`, j.EntityID).
		Scan(&query, &keywordID, &prio, &lastAPI, &lastWeb)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil // the keyword was removed
	}
	if err != nil {
		return time.Time{}, err
	}
	now := r.now()
	// Website search: newest first, stopping at results already found.
	webHits, err := r.webSearch(ctx, query)
	if err != nil {
		return time.Time{}, err
	}
	nWeb, err := r.record(ctx, "web", keywordID, webHits)
	if err != nil {
		return time.Time{}, err
	}
	nAPI := 0
	apiDue := r.API != nil && prio >= 1 && (lastAPI == nil || now.Sub(*lastAPI) >= 3*time.Hour)
	if apiDue {
		cap := r.QuotaCap
		if cap <= 0 {
			cap = DefaultQuotaCap
		}
		ok, err := Reserve(ctx, r.Pool, now, ytapi.CostSearch, cap)
		if err != nil {
			return time.Time{}, err
		}
		if ok {
			since := now.Add(-3*time.Hour - 30*time.Minute)
			if lastAPI != nil && lastAPI.Before(since) {
				since = lastAPI.Add(-30 * time.Minute)
			}
			res, err := r.API.Search(ctx, query, ytapi.SearchOptions{RegionCode: "IQ", RelevanceLanguage: "ar", MaxResults: 50, PublishedAfter: since})
			if err != nil && !errors.Is(err, ytapi.ErrQuota) {
				r.Log.Warn("API search failed", "err", err)
			} else if res != nil {
				var hs []hit
				for _, h := range res.Hits {
					hs = append(hs, hit{videoID: h.VideoID, channelID: h.ChannelID, title: h.Title, published: h.PublishedAt})
				}
				if nAPI, err = r.record(ctx, "api", keywordID, hs); err != nil {
					return time.Time{}, err
				}
				_, _ = r.Pool.Exec(ctx, `UPDATE search_terms SET last_api = $2 WHERE term = $1`, j.EntityID, now)
			}
		}
	}
	if _, err := r.Pool.Exec(ctx, `UPDATE search_terms SET last_web = $2, web_hits = web_hits + $3, api_hits = api_hits + $4
		WHERE term = $1`, j.EntityID, now, nWeb, nAPI); err != nil {
		return time.Time{}, err
	}
	if prio >= 1 {
		return now.Add(time.Hour), nil
	}
	return now.Add(6 * time.Hour), nil
}

func (r *Runner) webParams(ctx context.Context) string {
	var p string
	if ok, err := db.GetSetting(ctx, r.Pool, "search_web_params", &p); err == nil && ok {
		return p
	}
	return DefaultWebParams
}

func (r *Runner) webSearch(ctx context.Context, query string) ([]hit, error) {
	pages := r.WebPages
	if pages <= 0 {
		pages = 5
	}
	payload := map[string]any{"query": query}
	if p := r.webParams(ctx); p != "" {
		payload["params"] = p
	}
	var out []hit
	prefer := ""
	for page := 0; page < pages; page++ {
		res, err := r.Fetch.Client.CallPrefer(ctx, innertube.Search, payload, prefer)
		if err != nil {
			if page > 0 {
				return out, nil // keep what the first pages found
			}
			return nil, err
		}
		prefer = res.Proxy
		l := ytparse.ParseList(res.Data, r.now())
		known := 0
		for _, v := range l.Videos {
			if v.VideoID == "" || v.ChannelID == "" {
				continue
			}
			h := hit{videoID: v.VideoID, channelID: v.ChannelID, title: v.Title}
			if v.Published.OK {
				h.published = v.Published.At
			}
			out = append(out, h)
			var seen bool
			_ = r.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM search_hits WHERE engine = 'web' AND video_id = $1)`, v.VideoID).Scan(&seen)
			if seen {
				known++
			}
		}
		if l.NextToken == "" || len(l.Videos) == 0 || known == len(l.Videos) {
			break // reached what the last run found
		}
		payload = map[string]any{"continuation": l.NextToken}
	}
	return out, nil
}

// record stores the hits, queues new uploads of listed channels, and makes
// candidates of channels not on the list. It returns how many hits were new.
func (r *Runner) record(ctx context.Context, engine string, keywordID int64, hs []hit) (int, error) {
	fresh := 0
	for _, h := range hs {
		if h.channelID == "" || r.Sup != nil && (r.Sup.Suppressed(privacy.Channel, h.channelID) || r.Sup.Suppressed(privacy.Video, h.videoID)) {
			continue
		}
		var published *time.Time
		if !h.published.IsZero() {
			published = &h.published
		}
		tag, err := r.Pool.Exec(ctx, `INSERT INTO search_hits (engine, keyword_id, video_id, channel_id, title, published_at, found_at)
			SELECT $1, $2, $3, $4, $5, $6, $7
			WHERE NOT EXISTS (SELECT 1 FROM search_hits WHERE engine = $1 AND video_id = $3 AND keyword_id = $2)`,
			engine, keywordID, h.videoID, h.channelID, nz(h.title), published, r.now())
		if err != nil {
			return fresh, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		fresh++
		var status string
		err = r.Pool.QueryRow(ctx, `SELECT status FROM channels WHERE id = $1`, h.channelID).Scan(&status)
		switch {
		case err == nil && status == "listed":
			// A listed channel's upload seen in search: make sure we have it.
			item := ytparse.VideoItem{VideoID: h.videoID, Title: h.title, Kind: ytparse.KindVideo}
			if !h.published.IsZero() {
				item.Published = ytparse.TimeEstimate{At: h.published, OK: true, Precision: 24 * time.Hour}
			}
			src := "web_search"
			if engine == "api" {
				src = "api_search"
			}
			isNew, err := r.Write.ListedVia(ctx, h.channelID, item, src)
			if err != nil && !errors.Is(err, writer.ErrSuppressed) {
				return fresh, err
			}
			if isNew {
				if err := queue.Now(ctx, r.Pool, queue.KindVideo, h.videoID, h.channelID, queue.P1); err != nil {
					return fresh, err
				}
			}
		case err == nil:
			// Known but not listed (unlisted by you, or removed): leave it.
		case errors.Is(err, pgx.ErrNoRows):
			src := engine + "_search"
			if err := r.decide(ctx, h.channelID, `INSERT INTO candidates (channel_id, sources) VALUES ($1, ARRAY[$2])
				ON CONFLICT (channel_id) DO UPDATE SET hits = candidates.hits + 1, last_found = now(),
					sources = CASE WHEN $2 = ANY(candidates.sources) THEN candidates.sources ELSE array_append(candidates.sources, $2::text) END`,
				h.channelID, src); err != nil {
				return fresh, err
			}
			if _, err := queue.Ensure(ctx, r.Pool, queue.KindCandidate, h.channelID, h.channelID, queue.P3, r.now()); err != nil {
				return fresh, err
			}
		default:
			return fresh, err
		}
	}
	return fresh, nil
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Decision is the outcome of a candidate check.
type Decision struct {
	List     bool
	Kind     string // registry.Core or registry.Mention
	Tier     policy.Tier
	Matching int // matching items found
	Checked  int // items looked at
	Why      string
}

// Classify applies plan §5.4 to what a candidate check read.
func Classify(country string, ownPageMatches bool, recentTitles []string, hitTitles []string, match func(string) bool,
	subscribers int64, newestUpload time.Time, now time.Time) Decision {
	d := Decision{}
	recentMatching := 0
	for _, t := range recentTitles {
		if match(t) {
			recentMatching++
		}
	}
	hitMatching := 0
	for _, t := range hitTitles {
		if match(t) {
			hitMatching++
		}
	}
	d.Checked = len(recentTitles) + len(hitTitles)
	d.Matching = recentMatching + hitMatching
	iraq := strings.Contains(strings.ToLower(country), "iraq") || strings.Contains(country, "العراق") || strings.Contains(country, "عێراق")
	if d.Matching == 0 && !ownPageMatches && !iraq {
		d.Why = fmt.Sprintf("no matching item in %d looked at", d.Checked)
		return d
	}
	d.List = true
	share := 0.0
	if len(recentTitles) > 0 {
		share = float64(recentMatching) / float64(len(recentTitles))
	}
	switch {
	case iraq:
		d.Kind, d.Why = registry.Core, "country is Iraq"
	case share >= 0.3:
		d.Kind, d.Why = registry.Core, fmt.Sprintf("%d%% of recent items match", int(share*100))
	case ownPageMatches:
		d.Kind, d.Why = registry.Core, "the channel's own page matches the keywords"
	default:
		d.Kind, d.Why = registry.Mention, fmt.Sprintf("%d matching item(s)", d.Matching)
	}
	if d.Kind == registry.Mention {
		d.Tier = policy.M
	} else {
		d.Tier = TierFor(subscribers, newestUpload, now)
	}
	return d
}

// TierFor assigns a core channel's tier by size and activity (plan §6.1):
// A large (≥ 1M subscribers), B medium (≥ 100K), C small and active, D
// dormant (nothing uploaded in 180 days).
func TierFor(subscribers int64, newestUpload, now time.Time) policy.Tier {
	if !newestUpload.IsZero() && now.Sub(newestUpload) > 180*24*time.Hour {
		return policy.D
	}
	switch {
	case subscribers >= 1_000_000:
		return policy.A
	case subscribers >= 100_000:
		return policy.B
	default:
		return policy.C
	}
}

// candidate checks a channel discovery found.
func (r *Runner) candidate(ctx context.Context, j queue.Job) (time.Time, error) {
	id := j.EntityID
	if r.Sup != nil && r.Sup.Suppressed(privacy.Channel, id) {
		_, err := r.Pool.Exec(ctx, `DELETE FROM candidates WHERE channel_id = $1`, id)
		return time.Time{}, err
	}
	var status string
	if err := r.Pool.QueryRow(ctx, `SELECT status FROM channels WHERE id = $1`, id).Scan(&status); err == nil && status == "listed" {
		return time.Time{}, r.decide(ctx, id, `UPDATE candidates SET status = 'listed', checked_at = now() WHERE channel_id = $1`, id)
	}
	var cost phase0.Cost
	ch, err := r.Fetch.Channel(ctx, id, &cost)
	if errors.Is(err, innertube.ErrNotFound) {
		return time.Time{}, r.decide(ctx, id, `UPDATE candidates SET status = 'no_match', checked_at = now(), decision = 'channel not found'
			WHERE channel_id = $1`, id)
	}
	if err != nil {
		return time.Time{}, err
	}
	var recent []string
	var newest time.Time
	if tab, ok := ch.Channel.Tabs["videos"]; ok {
		if tr, err := r.Fetch.Tab(ctx, id, tab.Params, time.Time{}, 1, &cost); err == nil {
			for _, it := range tr.Items {
				recent = append(recent, it.Title)
				if it.Published.OK && it.Published.At.After(newest) {
					newest = it.Published.At
				}
			}
		}
	}
	var hitTitles []string
	rows, err := r.Pool.Query(ctx, `SELECT COALESCE(title, '') FROM search_hits WHERE channel_id = $1 LIMIT 50`, id)
	if err == nil {
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil && t != "" {
				hitTitles = append(hitTitles, t)
			}
		}
		rows.Close()
	}
	match := func(s string) bool { ok, _ := r.Write.Match(s); return ok }
	own := match(ch.Channel.Title + "\n" + ch.Channel.Description + "\n" + ch.Channel.Keywords + "\n" + ch.About.Description)
	subs := ch.About.Subscribers.Min
	if !ch.About.Subscribers.OK {
		subs = ch.Channel.Subscribers.Min
	}
	d := Classify(ch.About.Country, own, recent, hitTitles, match, subs, newest, r.now())
	if !d.List {
		if err := r.decide(ctx, id, `UPDATE candidates SET status = 'no_match', checked_at = now(), decision = $2 WHERE channel_id = $1`,
			id, d.Why); err != nil {
			return time.Time{}, err
		}
		return r.now().Add(90 * 24 * time.Hour), nil // check again later (plan §5.4)
	}
	err = registry.List(ctx, r.Pool, r.Sup, id, registry.Input{Input: id, Kind: d.Kind, Tier: d.Tier, AddedBy: "discovery", Reason: d.Why})
	if errors.Is(err, registry.ErrSuppressed) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if err := r.decide(ctx, id, `UPDATE candidates SET status = 'listed', checked_at = now(), decision = $2 WHERE channel_id = $1`,
		id, d.Kind+": "+d.Why); err != nil {
		return time.Time{}, err
	}
	r.Log.Info("discovery listed a channel", "channel", id, "kind", d.Kind, "tier", string(d.Tier), "why", d.Why)
	return time.Time{}, nil
}

// decide writes a candidate (found, or decided) and its outbox event in
// one transaction (fix 12).
func (r *Runner) decide(ctx context.Context, id, sql string, args ...any) error {
	ev := outbox.New(ctx, r.now())
	return pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
		_, err := ev.Content(ctx, tx, outbox.Candidates, id)
		return err
	})
}

// RecomputeTiers re-assigns the tier of every listed core channel whose
// tier was not set by hand (weekly, plan §6.1). It returns how many moved.
func RecomputeTiers(ctx context.Context, q db.Querier, now time.Time) (int, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.tier, COALESCE(c.subscribers_min, 0), max(v.published_at)
		FROM channels c LEFT JOIN videos v ON v.channel_id = c.id
		WHERE c.status = 'listed' AND c.kind = 'core' AND NOT c.tier_manual
		GROUP BY c.id`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id, tier string
		subs     int64
		newest   *time.Time
	}
	var all []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.tier, &x.subs, &x.newest); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, x)
	}
	rows.Close()
	moved := 0
	for _, x := range all {
		var newest time.Time
		if x.newest != nil {
			newest = *x.newest
		}
		t := TierFor(x.subs, newest, now)
		if string(t) == x.tier {
			continue
		}
		if _, err := q.Exec(ctx, `UPDATE channels SET tier = $2 WHERE id = $1`, x.id, string(t)); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// CommenterGraph records, from stored comments, which channels each author
// comments on (relation "commenter", weight = comments). Enrichment turns
// commenters with uploads of their own into candidates (plan §5.3).
//
// Only new pairs and pairs whose count or last sighting changed are
// written (14 November: rewriting every pair each day made as much WAL and
// as many dead rows as the whole table, on a database under strain). It
// returns how many pairs were added or changed.
func CommenterGraph(ctx context.Context, q db.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `INSERT INTO channel_links_graph (from_channel, to_channel, relation, weight, first_seen, last_seen)
		SELECT author_id, channel_id, 'commenter', count(*), min(first_seen), max(last_seen)
		FROM comments WHERE author_id IS NOT NULL AND author_id <> channel_id GROUP BY author_id, channel_id
		ON CONFLICT (from_channel, to_channel, relation) DO UPDATE SET weight = EXCLUDED.weight, last_seen = EXCLUDED.last_seen
		WHERE channel_links_graph.weight IS DISTINCT FROM EXCLUDED.weight
			OR channel_links_graph.last_seen IS DISTINCT FROM EXCLUDED.last_seen`)
	return tag.RowsAffected(), err
}
