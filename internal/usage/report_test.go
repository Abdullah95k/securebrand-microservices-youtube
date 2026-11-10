package usage

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

// A synthetic 48-hour pilot of 4 channels, with numbers worked out by hand.
func TestBuildReport(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	from := time.Date(2026, 11, 10, 0, 0, 0, 0, Iraq) // 9 Nov 21:00 UTC
	to := from.Add(48 * time.Hour)
	for _, c := range []struct{ id, tier string }{
		{"UCfakeA1AAAAAAAAAAAAAAAA", "A"}, {"UCfakeA2AAAAAAAAAAAAAAAA", "A"},
		{"UCfakeC1AAAAAAAAAAAAAAAA", "C"}, {"UCfakeC2AAAAAAAAAAAAAAAA", "C"},
	} {
		exec(`INSERT INTO channels (id, tier, status) VALUES ($1, $2, 'listed')`, c.id, c.tier)
	}
	// Every hour: comments 100 and rss 20 requests (channel work),
	// enrichment 10, search 5; 10,000 bytes a request. Hour 10 is busier
	// (comments 300); hour 0 has a 400-request backfill. An hour outside
	// the window must not count.
	hourly := func(h time.Time, kind string, req int64) {
		exec(`INSERT INTO usage_hourly (hour, kind, jobs, failures, requests, bytes, blocks, run_ms) VALUES ($1, $2, 1, 0, $3, $4, 0, 1000)`,
			h, kind, req, req*10000)
	}
	for i := 0; i < 48; i++ {
		h := from.Add(time.Duration(i) * time.Hour)
		comments := int64(100)
		if i == 10 {
			comments = 300
		}
		hourly(h, queue.KindComments, comments)
		hourly(h, queue.KindRSS, 20)
		hourly(h, queue.KindEnrich, 10)
		hourly(h, queue.KindSearch, 5)
	}
	hourly(from, queue.KindBackfill, 400)
	hourly(from.Add(-2*time.Hour), queue.KindComments, 99999)
	exec(`UPDATE usage_hourly SET blocks = 2 WHERE kind = 'comments' AND hour = $1`, from)
	// Per channel per day (Iraq days 10 and 11 Nov): A channels 1,000 a day,
	// C channels 300 and 200; A1 and C1 backfilled 250 and 150 requests on
	// the first day.
	daily := func(day, id string, req, bfReq int64) {
		exec(`INSERT INTO usage_channel_daily (day, channel_id, jobs, requests, bytes, backfill_requests, backfill_bytes)
			VALUES ($1, $2, 10, $3, $4, $5, $6)`, day, id, req, req*10000, bfReq, bfReq*10000)
	}
	daily("2026-11-10", "UCfakeA1AAAAAAAAAAAAAAAA", 1250, 250)
	daily("2026-11-11", "UCfakeA1AAAAAAAAAAAAAAAA", 1000, 0)
	daily("2026-11-10", "UCfakeA2AAAAAAAAAAAAAAAA", 1000, 0)
	daily("2026-11-11", "UCfakeA2AAAAAAAAAAAAAAAA", 1000, 0)
	daily("2026-11-10", "UCfakeC1AAAAAAAAAAAAAAAA", 450, 150)
	daily("2026-11-11", "UCfakeC1AAAAAAAAAAAAAAAA", 300, 0)
	daily("2026-11-10", "UCfakeC2AAAAAAAAAAAAAAAA", 200, 0)
	daily("2026-11-11", "UCfakeC2AAAAAAAAAAAAAAAA", 200, 0)
	exec(`INSERT INTO settings (key, value) VALUES ('capacity/fake-worker', '{"proxies": 6, "interval_s": 2, "backfill_days": 7, "at": "2026-11-11T12:00:00Z"}')`)
	// The database grew 200 MB in 24 hours.
	exec(`INSERT INTO db_sizes (at, bytes, tables) VALUES ($1, 1000000000, '{"comments": 500000000, "videos": 100000000}'),
		($2, 1200000000, '{"comments": 650000000, "videos": 110000000}')`, from.Add(time.Hour), from.Add(25*time.Hour))

	r, err := Build(ctx, pool, Options{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 0.01*math.Max(1, math.Abs(want)) {
			t.Errorf("%s = %v; want %v", name, got, want)
		}
	}
	near("hours", r.Hours, 48)
	if r.Gap != "2s" || r.BackfillDepth != 7 {
		t.Errorf("gap %s, backfill depth %d; want the workers' 2s and 7 days", r.Gap, r.BackfillDepth)
	}
	if r.Total.Requests != 48*135+200+400 {
		t.Errorf("total requests = %d", r.Total.Requests)
	}
	if g := r.group(GroupChannel); g.Requests != 48*120+200 || g.Blocks != 2 {
		t.Errorf("channel group = %+v", g)
	}
	if r.PeakHourRequests != 335 || !r.PeakHour.Equal(from.Add(10*time.Hour)) {
		t.Errorf("peak hour %s with %d; want hour 10 with 335 (backfill left out)", r.PeakHour, r.PeakHourRequests)
	}
	// Per channel per day, without the backfill.
	if r.Channels != 4 || len(r.ChannelDays) != 2 || r.ChannelHours != 48 {
		t.Fatalf("channels %d, days %v, hours %v", r.Channels, r.ChannelDays, r.ChannelHours)
	}
	tiers := map[string]TierCost{}
	for _, x := range r.Tiers {
		tiers[x.Tier] = x
	}
	near("tier A mean", tiers["A"].Requests.Mean, 1000)
	near("tier C mean", tiers["C"].Requests.Mean, 250)
	near("tier C most", tiers["C"].Requests.Max, 300)
	near("all median", tiers["all"].Requests.Median, 300) // 200, 300, 1000, 1000: nearest rank
	near("tier A MB", tiers["A"].MB.Mean, 10)
	if r.BackfillChannels != 2 {
		t.Errorf("backfill channels = %d", r.BackfillChannels)
	}
	near("backfill per channel", r.BackfillPerChan.Mean, 200)
	// Projections.
	if len(r.Projections) != 3 || r.Projections[0].Channels != 4 {
		t.Fatalf("projections = %+v", r.Projections)
	}
	measured, p1k, p10k := r.Projections[0], r.Projections[1], r.Projections[2]
	near("measured per day", measured.RequestsPerDay, 3340)
	near("measured peak", measured.PeakHourRequests, 335)
	if measured.IPs != MinIPs {
		t.Errorf("measured IPs = %d; want the minimum %d", measured.IPs, MinIPs)
	}
	near("1k per day", p1k.RequestsPerDay, 805120)
	near("1k discovery (not scaled)", p1k.DiscoveryRequests, 120)
	near("1k GB a day", p1k.GBPerDay, 8.0512)
	near("1k peak", p1k.PeakHourRequests, 80753.05)
	// 80,753 an hour at 1,333 per IP (2 s gap + 0.7 s answer), 70% busy:
	// 86.5 IPs, plus 30% spare: 112.5, so 113.
	if p1k.IPs != 113 || p10k.IPs != 1125 {
		t.Errorf("IPs = %d and %d; want 113 and 1125", p1k.IPs, p10k.IPs)
	}
	near("1k backfill", p1k.BackfillRequests, 200000)
	near("1k backfill GB", p1k.BackfillGB, 2)
	near("1k full backfill", p1k.FullBackfill, 10428571.43)
	// 2,000,000 requests in 30 days at 933 an hour per IP: 2.98 IPs, plus
	// 30% spare: 3.9, so 4.
	if p1k.BackfillIPs != 1 || p10k.BackfillIPs != 4 {
		t.Errorf("backfill IPs = %d and %d; want 1 and 4", p1k.BackfillIPs, p10k.BackfillIPs)
	}
	if r.Proxies != 6 || r.PeakUse > 0.05 {
		t.Errorf("proxies %d, busiest hour use %.3f; want 6 and about 4%%", r.Proxies, r.PeakUse)
	}
	for _, n := range r.Notes {
		if strings.Contains(n, "they were full") {
			t.Errorf("a quiet window reported as full: %s", n)
		}
	}
	near("1k database GB a year", p1k.DBGBPerYear, 18250)
	// Database growth.
	if r.DB == nil {
		t.Fatal("no database growth")
	}
	near("database per day", r.DB.PerDay, 2e8)
	if len(r.DB.Tables) != 2 || r.DB.Tables[0].Table != "comments" || r.DB.Tables[0].Bytes != 150000000 {
		t.Errorf("table growth = %+v", r.DB.Tables)
	}
	// The Markdown has the headline numbers, in Iraq time, and no IDs.
	md := r.Markdown()
	for _, want := range []string{
		"Window: 10 Nov 2026 00:00 to 12 Nov 2026 00:00, Iraq time.",
		"| A | 2 | 1,000 |",
		"| 1,000 | 805,120 |",
		"| 10,000 | 8,050,120 |",
		"| 4 (measured) |",
		"the busiest hour was 10 Nov 2026 10:00 with 335",
		"200.0 MB a day",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "UCfake") {
		t.Error("the report must not list channel IDs")
	}
}

func TestBuildReportEmpty(t *testing.T) {
	pool := dbtest.New(t)
	now := time.Now()
	r, err := Build(context.Background(), pool, Options{From: now.Add(-48 * time.Hour), To: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.Hours != 0 || len(r.Projections) != 0 || r.Gap != DefaultGap.String() {
		t.Errorf("empty report = %+v", r)
	}
	if md := r.Markdown(); !strings.Contains(md, "No jobs were recorded in this window.") {
		t.Errorf("Markdown = %s", md)
	}
	if _, err := Build(context.Background(), pool, Options{From: now, To: now}); err == nil {
		t.Error("an empty window must be refused")
	}
}

func TestNum(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4321: "-4,321"} {
		if got := num(in, 0); got != want {
			t.Errorf("num(%v) = %s; want %s", in, got, want)
		}
	}
	if got := num(1234.567, 2); got != "1,234.57" {
		t.Errorf("num = %s", got)
	}
}

func TestIPsFor(t *testing.T) {
	// 2 s gap and a 0.7 s answer: 1,333 an hour per IP, 933 at 70%; then
	// 30% spare. The pilot's busiest hour, 13,210 requests: 18.4, so 19.
	if n := ipsFor(13210, 2*time.Second); n != 19 {
		t.Errorf("ipsFor = %d; want 19", n)
	}
	if n := ipsFor(10, 2*time.Second); n != MinIPs {
		t.Errorf("ipsFor = %d; want the minimum %d", n, MinIPs)
	}
}

// The pilot's busiest hour, 13,210 requests on 10 IPs at a 2 s gap, is 99%
// of what they can make: the report says the counts are a floor.
func TestFullBusiestHour(t *testing.T) {
	r := Report{Channels: 2646, Hours: 48, Gap: "2s", Proxies: 10, PeakHourRequests: 13210,
		Groups: []KindCost{{Kind: GroupChannel, Cost: Cost{Requests: 369498}}}}
	r.project(nil)
	if r.PeakUse < 0.98 || r.PeakUse > 1 {
		t.Errorf("busiest hour use = %.3f; want 0.99", r.PeakUse)
	}
	full := false
	for _, n := range r.Notes {
		full = full || strings.Contains(n, "they were full")
	}
	if !full {
		t.Errorf("no note that the IPs were full: %q", r.Notes)
	}
}
