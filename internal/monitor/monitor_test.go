package monitor_test

import (
	"context"
	"errors"
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
	reg := metrics.New()
	if err := monitor.DB(ctx, pool, reg, now); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	_, _ = reg.WriteTo(&b)
	out := b.String()
	for _, want := range []string{
		`yt_channels_listed{kind="core",tier="A"} 1`,
		`yt_queue_due{kind="rss",priority="2"} 1`,
		`yt_fill_rate{endpoint="watch",field="title"} 0.9`,
		`yt_detection_delay_seconds{source="rss"}`,
		`yt_erasure_requests{status="pending"} 1`,
		"yt_erasure_oldest_pending_seconds 2592",
		"yt_comments_complete_pct 80",
		"yt_capacity_supply_per_hour 1440",
		"yt_suppression_violations 0",
		"yt_db_size_bytes",
		`yt_archive_bytes{table="comment_versions"} 1234`,
		"yt_archive_unscrubbed_seconds 0",
		`yt_partition_oldest_age_days{table="channel_snapshots"}`,
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
