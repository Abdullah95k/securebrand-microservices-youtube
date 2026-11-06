// Package monitor turns the system's state into Prometheus metrics (plan
// §8.13): the job queue, freshness and capacity, the API quota, comment
// completeness, fill rates and parse failures, discovery, privacy
// (erasure age, suppression violations), and each worker's proxy pool. It
// also keeps proxy health in the database (without credentials) and runs
// the canary checks.
package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fill"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
)

// AuditKey stores the last privacy audit's result for the metrics.
const AuditKey = "privacy_audit_last"

// AuditResult is what the scheduler stores after each audit.
type AuditResult struct {
	Violations int       `json:"violations"`
	At         time.Time `json:"at"`
}

type gauge struct {
	name, help string
	sql        string
	args       []any
	labels     []string // column names of the label columns, in order
}

// DB refreshes the database-derived metrics.
func DB(ctx context.Context, pool *pgxpool.Pool, reg *metrics.Registry, now time.Time) error {
	day := now.Add(-24 * time.Hour)
	gauges := []gauge{
		{"yt_queue_jobs", "Jobs in the queue.", `SELECT kind, priority::text, count(*) FROM schedule GROUP BY 1, 2`, nil, []string{"kind", "priority"}},
		{"yt_queue_due", "Jobs due now and not running.", `SELECT kind, priority::text, count(*) FROM schedule
			WHERE due_at <= now() AND dead_at IS NULL AND (lease_until IS NULL OR lease_until < now()) GROUP BY 1, 2`, nil, []string{"kind", "priority"}},
		{"yt_queue_running", "Jobs running.", `SELECT kind, count(*) FROM schedule WHERE lease_until >= now() GROUP BY 1`, nil, []string{"kind"}},
		{"yt_queue_failing", "Jobs whose last run failed (dead jobs not counted).", `SELECT kind, count(*) FROM schedule
			WHERE last_error IS NOT NULL AND dead_at IS NULL GROUP BY 1`, nil, []string{"kind"}},
		{"yt_queue_dead", "Jobs dead after too many failures: not retried until requeued.", `SELECT kind, count(*) FROM schedule
			WHERE dead_at IS NOT NULL GROUP BY 1`, nil, []string{"kind"}},
		{"yt_queue_oldest_due_seconds", "How long the oldest due job has waited.", `SELECT kind,
			COALESCE(EXTRACT(EPOCH FROM now() - min(due_at)), 0) FROM schedule WHERE due_at <= now() AND dead_at IS NULL GROUP BY 1`, nil, []string{"kind"}},
		{"yt_channels_listed", "Listed channels.", `SELECT kind, tier, count(*) FROM channels WHERE status = 'listed' GROUP BY 1, 2`, nil, []string{"kind", "tier"}},
		{"yt_channels_by_status", "Channels by status.", `SELECT status, count(*) FROM channels GROUP BY 1`, nil, []string{"status"}},
		{"yt_videos_new_24h", "Videos first seen in the last 24 hours.", `SELECT source, count(*) FROM videos WHERE first_seen >= $1 GROUP BY 1`, []any{day}, []string{"source"}},
		{"yt_comments_new_24h", "Comments first seen in the last 24 hours.", `SELECT count(*) FROM comments WHERE first_seen >= $1`, []any{day}, nil},
		{"yt_comments_complete_pct", "Average comment completeness of videos checked in 7 days.", `SELECT COALESCE(avg(comments_complete_pct), 100)
			FROM videos WHERE comments_complete_pct IS NOT NULL AND last_checked >= $1`, []any{now.Add(-7 * 24 * time.Hour)}, nil},
		{"yt_comments_partial_videos", "Videos whose comments hit the cap.", `SELECT count(*) FROM videos WHERE comments_partial`, nil, nil},
		{"yt_detection_delay_seconds", "Average delay from publish to first seen, last 24 hours, exact dates only.", `SELECT source,
			COALESCE(avg(EXTRACT(EPOCH FROM first_seen - published_at)), 0) FROM videos
			WHERE first_seen >= $1 AND published_precision_s = 0 AND published_at <= first_seen GROUP BY 1`, []any{day}, []string{"source"}},
		{"yt_parse_failures_open", "Parse failures not resolved.", `SELECT endpoint, count(*) FROM parse_failures WHERE resolved_at IS NULL GROUP BY 1`, nil, []string{"endpoint"}},
		{"yt_parse_failures_24h", "Parse failures in the last 24 hours.", `SELECT count(*) FROM parse_failures WHERE at >= $1`, []any{day}, nil},
		{"yt_fill_rate", "Share of readings with the field present, last 3 hours.", `SELECT endpoint, field,
			sum(present)::float / NULLIF(sum(seen), 0) FROM field_fill_rates WHERE window_start >= $1 GROUP BY 1, 2`,
			[]any{now.Add(-3 * time.Hour)}, []string{"endpoint", "field"}},
		{"yt_fill_rate_baseline", "The same over the 7 days before.", `SELECT endpoint, field,
			sum(present)::float / NULLIF(sum(seen), 0) FROM field_fill_rates WHERE window_start >= $1 AND window_start < $2 GROUP BY 1, 2`,
			[]any{now.Add(-7*24*time.Hour - 3*time.Hour), now.Add(-3 * time.Hour)}, []string{"endpoint", "field"}},
		{"yt_erasure_requests", "Erasure requests by status.", `SELECT status, count(*) FROM erasure_requests GROUP BY 1`, nil, []string{"status"}},
		{"yt_erasure_oldest_pending_seconds", "Age of the oldest pending erasure request.", `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(received_at)), 0)
			FROM erasure_requests WHERE status = 'pending'`, nil, nil},
		{"yt_suppression_entries", "Active suppression list entries.", `SELECT kind, count(*) FROM suppression_list WHERE override_at IS NULL GROUP BY 1`, nil, []string{"kind"}},
		{"yt_enrichment_backlog", "Commenters waiting for enrichment.", `SELECT count(*) FROM schedule WHERE kind = 'enrich' AND due_at <= now() AND dead_at IS NULL`, nil, nil},
		{"yt_discovery_candidates", "Discovery candidates by status.", `SELECT status, count(*) FROM candidates GROUP BY 1`, nil, []string{"status"}},
		{"yt_discovery_listed_24h", "Channels discovery listed in the last 24 hours.", `SELECT kind, count(*) FROM channels
			WHERE added_by = 'discovery' AND first_seen >= $1 GROUP BY 1`, []any{day}, []string{"kind"}},
		{"yt_keyword_suggestions_new", "Keyword suggestions waiting for a decision.", `SELECT count(*) FROM keyword_suggestions WHERE status = 'new'`, nil, nil},
		{"yt_keywords_low_precision", "Keywords flagged for low precision today.", `SELECT count(*) FROM keyword_stats
			WHERE day = $1::date AND matches >= 20 AND confirmed::float / matches < 0.3`, []any{now.UTC().Format("2006-01-02")}, nil},
		{"yt_db_size_bytes", "Database size.", `SELECT pg_database_size(current_database())`, nil, nil},
		{"yt_webhook_deliveries", "Webhook deliveries by endpoint and status (dead: gave up).",
			`SELECT endpoint, status, count(*) FROM webhook_deliveries GROUP BY 1, 2`, nil, []string{"endpoint", "status"}},
		{"yt_webhook_oldest_pending_seconds", "Age of the oldest event still waiting to be delivered, by endpoint.",
			`SELECT dl.endpoint, EXTRACT(EPOCH FROM now() - min(ev.created_at)) FROM webhook_deliveries dl
			JOIN webhook_events ev ON ev.id = dl.event_id WHERE dl.status = 'pending' GROUP BY 1`, nil, []string{"endpoint"}},
		{"yt_media_bytes", "Stored images, by kind.", `SELECT kind, COALESCE(sum(bytes), 0) FROM media GROUP BY kind`, nil, []string{"kind"}},
		{"yt_media_jobs_due", "Image downloads waiting.", `SELECT count(*) FROM schedule WHERE kind = 'media' AND due_at <= now() AND dead_at IS NULL`, nil, nil},
		{"yt_archive_bytes", "Archive size (compressed), by table.", `SELECT table_name, COALESCE(sum(bytes), 0) FROM archive_index GROUP BY table_name`, nil, []string{"table"}},
		{"yt_archive_rows", "Archived rows, by table.", `SELECT table_name, COALESCE(sum(rows), 0) FROM archive_months GROUP BY table_name`, nil, []string{"table"}},
		{"yt_archive_unscrubbed_seconds", "Age of the oldest erasure not yet applied to the archive (0: none).",
			`SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(s.created_at)), 0) FROM suppression_list s
			WHERE EXISTS (SELECT 1 FROM archive_index a WHERE a.scrubbed_at <= s.created_at)`, nil, nil},
		{"yt_partition_oldest_age_days", "Age of the oldest month still in Postgres, by table (archived after 12 months).",
			`SELECT p.relname, EXTRACT(EPOCH FROM now() - to_date(substring(c.relname from '_y(\d{4}m\d{2})$'), 'YYYY"m"MM')) / 86400
			FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class p ON p.oid = i.inhparent
			JOIN pg_namespace n ON n.oid = p.relnamespace
			WHERE n.nspname = current_schema() AND c.relname ~ '_y\d{4}m\d{2}$'
			AND p.relname IN ('channel_snapshots', 'video_snapshots', 'post_snapshots', 'comment_versions', 'author_profiles')
			AND to_date(substring(c.relname from '_y(\d{4}m\d{2})$'), 'YYYY"m"MM') = (
				SELECT min(to_date(substring(c2.relname from '_y(\d{4}m\d{2})$'), 'YYYY"m"MM'))
				FROM pg_inherits i2 JOIN pg_class c2 ON c2.oid = i2.inhrelid
				WHERE i2.inhparent = p.oid AND c2.relname ~ '_y\d{4}m\d{2}$')`, nil, []string{"table"}},
		{"yt_proxies_quarantined", "Proxy IPs in quarantine (all workers).", `SELECT count(*) FROM proxy_health WHERE state = 'quarantined'
			AND updated_at >= now() - interval '10 minutes'`, nil, nil},
		{"yt_proxies_cooling", "Proxy IPs resting after a block (all workers).", `SELECT count(*) FROM proxy_health WHERE cooldown_until > now()
			AND updated_at >= now() - interval '10 minutes'`, nil, nil},
	}
	for _, g := range gauges {
		if err := setFromQuery(ctx, pool, reg, g); err != nil {
			return fmt.Errorf("metric %s: %w", g.name, err)
		}
	}
	// API quota today.
	var used, cap int
	err := pool.QueryRow(ctx, `SELECT COALESCE(sum(units_used), 0), COALESCE(max(units_cap), 0) FROM quota_ledger WHERE day = $1::date`,
		ytapi.PacificDay(now)).Scan(&used, &cap)
	if err != nil {
		return err
	}
	reg.Set("yt_api_quota_used_units", "Official API units used today (Pacific day).", nil, float64(used))
	reg.Set("yt_api_quota_cap_units", "Today's official API ceiling.", nil, float64(cap))
	// Capacity: the latest planner report.
	var supply, demand float64
	var stretch float32
	var ips *int
	var debt []byte
	err = pool.QueryRow(ctx, `SELECT supply_per_hour, demand_per_hour, stretch, recommended_ips, debt FROM capacity_reports ORDER BY at DESC LIMIT 1`).
		Scan(&supply, &demand, &stretch, &ips, &debt)
	if err == nil {
		reg.Set("yt_capacity_supply_per_hour", "Requests per hour the proxies can do.", nil, supply)
		reg.Set("yt_capacity_demand_per_hour", "Requests per hour the schedule asks for.", nil, demand)
		reg.Set("yt_capacity_stretch", "Interval stretch for tiers C, D and M (1 = none).", nil, float64(stretch))
		if ips != nil {
			reg.Set("yt_capacity_recommended_ips", "Proxy IPs that would close the gap.", nil, float64(*ips))
		}
		reg.Reset("yt_freshness_debt_hours")
		var d map[string]float64
		if json.Unmarshal(debt, &d) == nil {
			for tier, h := range d {
				reg.Set("yt_freshness_debt_hours", "Overdue job-hours per tier.", map[string]string{"tier": tier}, h)
			}
		}
	}
	var audit AuditResult
	if ok, err := db.GetSetting(ctx, pool, AuditKey, &audit); err == nil && ok {
		reg.Set("yt_suppression_violations", "Suppressed items found in the database by the last audit (must be 0).", nil, float64(audit.Violations))
		reg.Set("yt_privacy_audit_timestamp_seconds", "When the last privacy audit ran.", nil, float64(audit.At.Unix()))
	}
	reg.Set("yt_monitor_timestamp_seconds", "When these metrics were refreshed.", nil, float64(now.Unix()))
	return nil
}

func setFromQuery(ctx context.Context, pool *pgxpool.Pool, reg *metrics.Registry, g gauge) error {
	rows, err := pool.Query(ctx, g.sql, g.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	reg.Reset(g.name)
	found := false
	for rows.Next() {
		vals := make([]any, len(g.labels)+1)
		strs := make([]*string, len(g.labels))
		for i := range g.labels {
			vals[i] = &strs[i]
		}
		var v *float64
		vals[len(g.labels)] = &v
		if err := rows.Scan(vals...); err != nil {
			return err
		}
		labels := map[string]string{}
		for i, l := range g.labels {
			if strs[i] != nil {
				labels[l] = *strs[i]
			}
		}
		val := 0.0
		if v != nil {
			val = *v
		}
		reg.Set(g.name, g.help, labels, val)
		found = true
	}
	if !found && len(g.labels) == 0 {
		reg.Set(g.name, g.help, nil, 0)
	}
	return rows.Err()
}

// Pool exports a worker's proxy pool and saves proxy health (opaque IDs
// and states only).
func Pool(ctx context.Context, q db.Querier, reg *metrics.Registry, owner string, st proxypool.Stats, now time.Time) error {
	reg.Set("yt_pool_speed", "Pool speed (1 = normal; lower while the brake is on).", map[string]string{"worker": owner}, st.Speed)
	reg.Set("yt_pool_pauses_total", "Times the brake paused every request.", map[string]string{"worker": owner}, float64(st.Pauses))
	reg.Set("yt_pool_subnet_cooldowns_total", "Times a whole subnet was rested.", map[string]string{"worker": owner}, float64(st.SubnetCooldowns))
	for c, cs := range st.Classes {
		l := map[string]string{"worker": owner, "class": string(c)}
		reg.Set("yt_pool_blocked_requests_total", "Requests that met at least one block.", l, float64(cs.BlockedRequests))
		reg.Set("yt_pool_failover_ok_total", "Blocked requests that succeeded on another IP.", l, float64(cs.FailoverOK))
		reg.Set("yt_pool_parked_total", "Requests given up after several IPs.", l, float64(cs.Parked))
	}
	healthy := 0
	for _, p := range st.Proxies {
		if Healthy(p) {
			healthy++
		}
	}
	wl := map[string]string{"worker": owner}
	reg.Set("yt_proxies_total", "Proxy IPs in this worker's pool.", wl, float64(len(st.Proxies)))
	reg.Set("yt_proxies_healthy", "Proxy IPs usable for page requests: not quarantined, not resting (player rests don't count).",
		wl, float64(healthy))
	for _, p := range st.Proxies {
		l := map[string]string{"worker": owner, "proxy": p.ID, "subnet": p.Subnet}
		reg.Set("yt_proxy_requests_total", "Requests per proxy IP.", l, float64(p.Requests))
		reg.Set("yt_proxy_blocks_total", "Block signals per proxy IP.", l, float64(p.Blocks))
		reg.Set("yt_proxy_bytes_total", "Bytes per proxy IP (requests and responses, compressed, on the wire).", l, float64(p.Bytes))
		state := "ok"
		var cool *time.Time
		for _, until := range p.CoolUntil {
			if cool == nil || until.After(*cool) {
				u := until
				cool = &u
			}
		}
		switch {
		case p.Quarantined:
			state = "quarantined"
		case cool != nil:
			state = "cooling"
		}
		q01 := 0.0
		if p.Quarantined {
			q01 = 1
		}
		reg.Set("yt_proxy_quarantined", "1 when the IP is in quarantine.", l, q01)
		if _, err := q.Exec(ctx, `INSERT INTO proxy_health (proxy_key, label, subnet, proxy_kind, state, cooldown_until, requests_24h, blocks_24h, updated_at)
			VALUES ($1, $1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (proxy_key) DO UPDATE SET subnet = EXCLUDED.subnet, proxy_kind = EXCLUDED.proxy_kind, state = EXCLUDED.state,
				cooldown_until = EXCLUDED.cooldown_until, requests_24h = EXCLUDED.requests_24h, blocks_24h = EXCLUDED.blocks_24h,
				updated_at = EXCLUDED.updated_at`,
			p.ID, p.Subnet, string(p.Kind), state, cool, p.Requests, p.Blocks, now); err != nil {
			return err
		}
	}
	return nil
}

// Healthy reports whether a proxy can serve page requests now: not
// quarantined, and not resting for any class but the player. The player is
// blocked often and asked sparingly (Phase 0 E1), so its rests don't make an
// IP unhealthy.
func Healthy(p proxypool.ProxyStats) bool {
	if p.Quarantined {
		return false
	}
	for c := range p.CoolUntil {
		if c != proxypool.ClassPlayer {
			return false
		}
	}
	return true
}

// Dead counts jobs that reached their failure limit (the worker's OnDead
// hook).
func Dead(reg *metrics.Registry) func(kind string) {
	return func(kind string) {
		reg.Add("yt_jobs_dead_total", "Jobs that reached their failure limit, by kind.", map[string]string{"kind": kind}, 1)
	}
}

// Raw counts raw responses (REQ-010 fix 8) by class (keep, 30d) and result
// (stored, failed, dropped), and the bytes stored.
func Raw(reg *metrics.Registry) func(class, result string, n int) {
	return func(class, result string, n int) {
		reg.Add("yt_raw_objects_total", "Raw responses, by class and result.", map[string]string{"class": class, "result": result}, 1)
		if result == "stored" {
			reg.Add("yt_raw_bytes_total", "Bytes of raw responses stored (gzipped), by class.", map[string]string{"class": class}, float64(n))
		}
	}
}

// Jobs counts job outcomes (the worker's OnResult hook).
func Jobs(reg *metrics.Registry) func(kind string, err error, took time.Duration) {
	return func(kind string, err error, took time.Duration) {
		result := "ok"
		if err != nil {
			result = "error"
		}
		reg.Add("yt_jobs_total", "Jobs run, by kind and result.", map[string]string{"kind": kind, "result": result}, 1)
		reg.Add("yt_job_seconds_total", "Time spent in jobs, by kind.", map[string]string{"kind": kind}, took.Seconds())
	}
}

// CanariesKey lists canary channel IDs in the settings; when it is unset,
// two listed channels are picked at random each round.
const CanariesKey = "canaries"

// Canary fetches a few public channels and their newest video, parses them
// and records only health: fill rates (endpoints "canary.*") and an ok
// gauge per check (plan §8.11).
func Canary(ctx context.Context, pool *pgxpool.Pool, fetch *phase0.Collector, rec *fill.Recorder, reg *metrics.Registry, log *slog.Logger) {
	var ids []string
	if ok, err := db.GetSetting(ctx, pool, CanariesKey, &ids); err != nil || !ok || len(ids) == 0 {
		rows, err := pool.Query(ctx, `SELECT id FROM channels WHERE status = 'listed' ORDER BY random() LIMIT 2`)
		if err != nil {
			return
		}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	okCh, okV := 0, 0
	for _, id := range ids {
		var cost phase0.Cost
		ch, err := fetch.Channel(ctx, id, &cost)
		good := err == nil && ch.Channel.Title != "" && len(ch.Channel.Tabs) > 0
		rec.Observe("canary.channel", "title", err == nil && ch.Channel.Title != "")
		rec.Observe("canary.channel", "tabs", err == nil && len(ch.Channel.Tabs) > 0)
		rec.Observe("canary.channel", "subscribers", err == nil && (ch.Channel.Subscribers.OK || ch.About.Subscribers.OK))
		if !good {
			continue
		}
		okCh++
		tab, has := ch.Channel.Tabs["videos"]
		if !has {
			continue
		}
		tr, err := fetch.Tab(ctx, id, tab.Params, time.Time{}, 1, &cost)
		if err != nil || len(tr.Items) == 0 {
			rec.Observe("canary.tab", "items", false)
			continue
		}
		rec.Observe("canary.tab", "items", true)
		v, err := fetch.Video(ctx, tr.Items[0].VideoID, false, &cost)
		vgood := err == nil && v.Watch.Title != ""
		rec.Observe("canary.watch", "title", vgood)
		rec.Observe("canary.watch", "views", err == nil && v.Watch.Views.OK)
		if vgood {
			okV++
		}
	}
	reg.Set("yt_canary_channels_ok", "Canary channels read and parsed in the last round.", nil, float64(okCh))
	reg.Set("yt_canary_videos_ok", "Canary videos read and parsed in the last round.", nil, float64(okV))
	reg.Set("yt_canary_targets", "Canary channels tried in the last round.", nil, float64(len(ids)))
	reg.Set("yt_canary_timestamp_seconds", "When the last canary round ran.", nil, float64(time.Now().Unix()))
	if len(ids) > 0 && okCh == 0 && log != nil {
		log.Warn("canary: no channel could be read and parsed", "tried", strconv.Itoa(len(ids)))
	}
}
