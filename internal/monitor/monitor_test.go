package monitor_test

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/capacity"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fill"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/monitor"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

func TestDatabaseMetrics(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Now()
	_ = registry.List(ctx, pool, nil, "UCfakeMONITOR00000000001", registry.Input{Input: "UCfakeMONITOR00000000001", Tier: policy.A})
	_, _ = pool.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, published_at, published_precision_s, first_seen, source, comments_complete_pct, last_checked)
		VALUES ('fakeMv1', 'UCfakeMONITOR00000000001', 'video', now() - interval '20 minutes', 0, now(), 'rss', 80, now())`)
	_, _ = pool.Exec(ctx, `INSERT INTO erasure_requests (kind, target_key, received_at) VALUES ('author', '\x01', now() - interval '3 days')`)
	rec := &fill.Recorder{}
	for i := 0; i < 10; i++ {
		rec.Observe("watch", "title", i != 0)
	}
	if err := rec.Flush(ctx, pool, now); err != nil {
		t.Fatal(err)
	}
	_ = capacity.Announce(ctx, pool, "w1", capacity.Worker{Proxies: 2, IntervalS: 4, At: now})
	if _, err := capacity.Plan(ctx, pool, now); err != nil {
		t.Fatal(err)
	}
	_ = db.SetSetting(ctx, pool, monitor.AuditKey, monitor.AuditResult{Violations: 0, At: now})
	if err := db.EnsurePartitions(ctx, pool, now, 0); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `INSERT INTO archive_index (table_name, month, part, object_key, rows, bytes, sha256, scrubbed_at)
		VALUES ('comment_versions', '2025-01-01', 1, 'k', 10, 1234, 'x', now())`)
	// Two thumbnails: one deleted after analysis, one past its deadline.
	_, _ = pool.Exec(ctx, `INSERT INTO media (kind, entity_id, sha256, object_key, source_url, content_type, bytes, first_seen, last_seen,
		announced_at, delete_after, attempt, deleted_at, delete_reason) VALUES
		('thumb', 'fakeMONv1', 'a', 'k1', 'x', 'image/jpeg', 100, $1, $1, $1, $1, 1, $1, 'processed'),
		('thumb', 'fakeMONv2', 'b', 'k2', 'x', 'image/jpeg', 200, $1, $1, $1, $1, 1, NULL, NULL)`, now.Add(-3*time.Hour))
	// The outbox: one event refused 85 hours ago, one waiting 10 minutes.
	// The backlog's age is the waiting one's; the refused one is counted
	// apart (10 November: 3 refused events kept OutboxBacklog firing).
	_, _ = pool.Exec(ctx, `INSERT INTO event_outbox (kind, entity_id, partition_key, observed_at, payload, created_at, failure) VALUES
		('channel', 'UCfakeMONITOR00000000001', 'UCfakeMONITOR00000000001', now(), '{}', now() - interval '85 hours', 'refused'),
		('comment', 'fakeMONc1', 'fakeMv1', now(), '{}', now() - interval '10 minutes', NULL)`)
	reg := metrics.New()
	if err := monitor.DB(ctx, pool, reg, now); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	_, _ = reg.WriteTo(&b)
	out := b.String()
	if m := regexp.MustCompile(`(?m)^yt_outbox_oldest_unsent_seconds (\S+)$`).FindStringSubmatch(out); m == nil {
		t.Error("no yt_outbox_oldest_unsent_seconds")
	} else if v, _ := strconv.ParseFloat(m[1], 64); v < 500 || v > 900 {
		t.Errorf("oldest waiting event %v s old; want the waiting one's 600, not the refused one's", v)
	}
	for _, want := range []string{
		"yt_outbox_failed 1",
		`yt_channels_listed{kind="core",tier="A"} 1`,
		`yt_queue_due{kind="rss",priority="0"} 1`,
		`yt_fill_rate{endpoint="watch",field="title"} 0.9`,
		`yt_detection_delay_seconds{source="rss"}`,
		`yt_erasure_requests{status="pending"} 1`,
		"yt_erasure_oldest_pending_seconds 2592",
		"yt_comments_complete_pct 80",
		"yt_capacity_supply_per_hour 1072.34", // 2 IPs at 4 s + 0.7 s, 70% busy
		"yt_capacity_spare_debt_hours 0",
		"yt_capacity_fixed_per_hour ",
		"yt_suppression_violations 0",
		"yt_db_size_bytes",
		`yt_archive_bytes{table="comment_versions"} 1234`,
		"yt_archive_unscrubbed_seconds 0",
		`yt_partition_oldest_age_days{table="channel_snapshots"}`,
		`yt_media_bytes{kind="thumb"} 200`,
		`yt_media_handoff{kind="thumb",state="deleted_processed"} 1`,
		`yt_media_handoff{kind="thumb",state="announced"} 1`,
		"yt_media_past_deadline 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics miss %q", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
}

func TestPoolMetricsAndProxyHealth(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Now()
	st := proxypool.Stats{Speed: 0.5, Classes: map[proxypool.Class]proxypool.ClassStats{proxypool.ClassBrowse: {BlockedRequests: 3, FailoverOK: 2}},
		Proxies: []proxypool.ProxyStats{
			{ID: "px-aaaa", Subnet: "sn-1", Kind: "isp", Requests: 100, Blocks: 2, Bytes: 5_000_000,
				CoolUntil: map[proxypool.Class]time.Time{proxypool.ClassPlayer: now.Add(time.Hour)}},
			{ID: "px-bbbb", Subnet: "sn-2", Kind: "isp", Requests: 50, Quarantined: true},
			{ID: "px-cccc", Subnet: "sn-3", Kind: "isp", Requests: 10,
				CoolUntil: map[proxypool.Class]time.Time{proxypool.ClassBrowse: now.Add(time.Hour)}},
		}}
	reg := metrics.New()
	if err := monitor.Pool(ctx, pool, reg, "w1", st, now); err != nil {
		t.Fatal(err)
	}
	var states string
	_ = pool.QueryRow(ctx, `SELECT string_agg(proxy_key || '=' || state, ',' ORDER BY proxy_key) FROM proxy_health`).Scan(&states)
	if states != "px-aaaa=cooling,px-bbbb=quarantined,px-cccc=cooling" {
		t.Errorf("proxy health %q", states)
	}
	var b strings.Builder
	_, _ = reg.WriteTo(&b)
	if !strings.Contains(b.String(), `yt_pool_speed{worker="w1"} 0.5`) || !strings.Contains(b.String(), `yt_proxy_quarantined{proxy="px-bbbb",subnet="sn-2",worker="w1"} 1`) {
		t.Errorf("pool metrics:\n%s", b.String())
	}
	if strings.Contains(b.String(), "@") || strings.Contains(b.String(), "password") {
		t.Error("metrics must carry no credentials")
	}
	// Fix 4: bytes per proxy. Fix 7: a player rest leaves an IP healthy;
	// quarantine or a page-class rest doesn't.
	for _, want := range []string{
		`yt_proxy_bytes_total{proxy="px-aaaa",subnet="sn-1",worker="w1"} 5e+06`,
		`yt_proxies_total{worker="w1"} 3`,
		`yt_proxies_healthy{worker="w1"} 1`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %s in:\n%s", want, b.String())
		}
	}
	monitor.Dead(reg)("media")
	b.Reset()
	_, _ = reg.WriteTo(&b)
	if !strings.Contains(b.String(), `yt_jobs_dead_total{kind="media"} 1`) {
		t.Error("dead jobs must be counted")
	}
	hook := monitor.Jobs(reg)
	hook("video", nil, time.Second)
	hook("video", errors.New("x"), time.Second)
	b.Reset()
	_, _ = reg.WriteTo(&b)
	if !strings.Contains(b.String(), `yt_jobs_total{kind="video",result="error"} 1`) {
		t.Error("job outcomes must be counted")
	}
	// Fix 8: raw responses by class and result, and the bytes stored.
	raw := monitor.Raw(reg)
	raw("keep", "stored", 1000)
	raw("30d", "stored", 500)
	raw("30d", "dropped", 700)
	b.Reset()
	_, _ = reg.WriteTo(&b)
	for _, want := range []string{
		`yt_raw_objects_total{class="keep",result="stored"} 1`,
		`yt_raw_objects_total{class="30d",result="dropped"} 1`,
		`yt_raw_bytes_total{class="30d"} 500`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
}

// A quarantine is saved with the pool's health, restored by the next
// worker, and lifted by a release (ytcollect proxies release). A
// quarantine saved by the old rule, without quarantined_since, is never
// restored; an IP quarantined again after a release is.
func TestQuarantineSurvivesRestartAndRelease(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	ps, err := proxypool.ParseList([]string{"http://192.0.2.10:8080", "http://198.51.100.10:8080"})
	if err != nil {
		t.Fatal(err)
	}
	a, b := ps[0].ID(), ps[1].ID()
	newPool := func() *proxypool.Pool { return proxypool.New(ps, proxypool.Config{}, 1) }
	save := func(pp *proxypool.Pool) {
		t.Helper()
		if err := monitor.Pool(ctx, pool, metrics.New(), "w1", pp.Stats(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	quarantined := func(pp *proxypool.Pool) map[string]time.Time {
		out := map[string]time.Time{}
		for _, p := range pp.Stats().Proxies {
			if p.Quarantined {
				out[p.ID] = p.QuarantinedSince
			}
		}
		return out
	}
	since := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	pp := newPool()
	pp.Quarantine(a, since)
	save(pp)
	// b as the old rule left it: quarantined, with no time.
	if _, err := pool.Exec(ctx, `UPDATE proxy_health SET state = 'quarantined' WHERE proxy_key = $1`, b); err != nil {
		t.Fatal(err)
	}

	pp = newPool() // a restart
	if n, err := monitor.RestoreQuarantine(ctx, pool, pp); err != nil || n != 1 {
		t.Fatalf("restored %d (%v); want a only", n, err)
	}
	if q := quarantined(pp); len(q) != 1 || !q[a].Equal(since) {
		t.Fatalf("quarantined after the restart: %v", q)
	}
	save(pp)
	if released, err := monitor.ApplyReleases(ctx, pool, pp); err != nil || len(released) != 0 {
		t.Fatalf("released %v (%v) before any release", released, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE proxy_health SET released_at = now() WHERE proxy_key = $1`, a); err != nil {
		t.Fatal(err)
	}
	if released, err := monitor.ApplyReleases(ctx, pool, pp); err != nil || len(released) != 1 || released[0] != a {
		t.Fatalf("released %v (%v); want a", released, err)
	}
	if q := quarantined(pp); len(q) != 0 {
		t.Fatalf("still quarantined: %v", q)
	}
	save(pp)
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM proxy_health WHERE quarantined_since IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows still marked quarantined after the release", n)
	}
	if n, _ := monitor.RestoreQuarantine(ctx, pool, newPool()); n != 0 {
		t.Errorf("restored %d after the release", n)
	}

	// Quarantined again, later: that one is restored.
	pp.Quarantine(a, time.Now().Add(time.Minute))
	save(pp)
	if n, _ := monitor.RestoreQuarantine(ctx, pool, newPool()); n != 1 {
		t.Errorf("restored %d; a was quarantined again after its release", n)
	}
}
