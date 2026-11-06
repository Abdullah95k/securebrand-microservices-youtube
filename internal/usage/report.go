package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

// Sizing rules (plan §6.4): an IP is kept at most TargetUse busy, with
// Spare extra IPs for failover and rests, and never fewer than MinIPs (a
// request parked after blocks on 3 IPs needs IPs on 3 subnets to fail over).
const (
	TargetUse    = 0.7
	Spare        = 0.3
	MinIPs       = 5
	BackfillDays = 30 // the backfill of new channels is finished within this
)

// DefaultGap is the per-IP gap assumed when no worker has announced one.
const DefaultGap = 2 * time.Second

// Groups of job kinds, by how their cost grows.
const (
	GroupChannel   = "channel"   // a monitored channel's own work: grows with the channel count
	GroupFollows   = "follows"   // commenter enrichment and images: follows the channels' comments and uploads
	GroupDiscovery = "discovery" // search, candidate checks, resolving inputs: no caps, grows with search terms
	GroupBackfill  = "backfill"  // once per new channel
)

// Group is a job kind's group.
func Group(kind string) string {
	switch kind {
	case queue.KindBackfill:
		return GroupBackfill
	case queue.KindEnrich, queue.KindMedia:
		return GroupFollows
	case queue.KindSearch, queue.KindCandidate, queue.KindResolve:
		return GroupDiscovery
	}
	return GroupChannel
}

// Options choose the report's window and assumptions.
type Options struct {
	From, To time.Time     // the window; hours with no recorded jobs are left out
	Gap      time.Duration // per-IP gap; 0 = the workers' announced gap, else DefaultGap
	Targets  []int         // channel counts to project to; nil = 1,000 and 10,000
}

// Cost is what a set of jobs used.
type Cost struct {
	Jobs     int64 `json:"jobs"`
	Failures int64 `json:"failures"`
	Requests int64 `json:"requests"`
	Bytes    int64 `json:"bytes"`
	Blocks   int64 `json:"blocks"`
	RunMS    int64 `json:"run_ms"`
}

func (c *Cost) add(o Cost) {
	c.Jobs += o.Jobs
	c.Failures += o.Failures
	c.Requests += o.Requests
	c.Bytes += o.Bytes
	c.Blocks += o.Blocks
	c.RunMS += o.RunMS
}

// KindCost is one job kind's cost in the window.
type KindCost struct {
	Kind  string `json:"kind"`
	Group string `json:"group"`
	Cost
}

// Spread is the spread of per-channel daily values.
type Spread struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
	Max    float64 `json:"max"`
}

// TierCost is per channel per day, steady state (no backfill), by tier.
type TierCost struct {
	Tier     string `json:"tier"`
	Channels int    `json:"channels"`
	Requests Spread `json:"requests"`
	MB       Spread `json:"mb"`
}

// Projection is the estimate for a channel count.
type Projection struct {
	Channels          int     `json:"channels"`
	RequestsPerDay    float64 `json:"requests_per_day"` // steady state, all groups
	ChannelRequests   float64 `json:"channel_requests_per_day"`
	FollowsRequests   float64 `json:"follows_requests_per_day"`
	DiscoveryRequests float64 `json:"discovery_requests_per_day"`
	GBPerDay          float64 `json:"gb_per_day"`
	GBPerMonth        float64 `json:"gb_per_month"`
	PeakHourRequests  float64 `json:"peak_hour_requests"`
	IPs               int     `json:"ips"`                // steady state
	BackfillRequests  float64 `json:"backfill_requests"`  // once, at the measured depth
	BackfillGB        float64 `json:"backfill_gb"`        // once
	BackfillIPs       int     `json:"backfill_extra_ips"` // to finish it within BackfillDays
	FullBackfill      float64 `json:"full_backfill_requests,omitempty"`
	DBGBPerYear       float64 `json:"db_gb_per_year,omitempty"`
}

// TableGrowth is one table's growth in the window.
type TableGrowth struct {
	Table string `json:"table"`
	Bytes int64  `json:"bytes"`
}

// DBGrowth is the database's growth, from the hourly samples.
type DBGrowth struct {
	From      time.Time     `json:"from"`
	To        time.Time     `json:"to"`
	FromBytes int64         `json:"from_bytes"`
	ToBytes   int64         `json:"to_bytes"`
	PerDay    float64       `json:"bytes_per_day"`
	Tables    []TableGrowth `json:"tables"`
}

// Quality is what the window collected, and how well.
type Quality struct {
	NewVideos            int64        `json:"new_videos"`
	NewComments          int64        `json:"new_comments"`
	CompletenessVideos   int64        `json:"completeness_videos"`
	CompletenessMedian   *float64     `json:"completeness_median_pct"`
	CompletenessMean     *float64     `json:"completeness_mean_pct"`
	DetectionVideos      int64        `json:"detection_videos"`
	DetectionMedianMin   *float64     `json:"detection_median_minutes"`
	DetectionP90Min      *float64     `json:"detection_p90_minutes"`
	DetectionBySourceMin []SourceTime `json:"detection_by_source"`
	Dead                 []queue.Dead `json:"dead_jobs"`
}

// SourceTime is a median detection delay per source.
type SourceTime struct {
	Source    string  `json:"source"`
	Videos    int64   `json:"videos"`
	MedianMin float64 `json:"median_minutes"`
}

// Report is "ytcollect report".
type Report struct {
	From             time.Time    `json:"from"`
	To               time.Time    `json:"to"`
	DataFrom         time.Time    `json:"data_from"` // first and last hour with jobs
	DataTo           time.Time    `json:"data_to"`
	Hours            float64      `json:"hours"`
	Gap              string       `json:"gap"`
	GapSource        string       `json:"gap_source"`
	BackfillDepth    int          `json:"backfill_depth_days,omitempty"` // as the workers announced it
	Kinds            []KindCost   `json:"kinds"`
	Groups           []KindCost   `json:"groups"`
	Total            Cost         `json:"total"`
	Channels         int          `json:"channels"` // monitored channels with jobs in the window
	ChannelDays      []string     `json:"channel_days"`
	ChannelHours     float64      `json:"channel_hours"`
	Tiers            []TierCost   `json:"tiers"`
	BackfillChannels int          `json:"backfill_channels"`
	BackfillPerChan  Spread       `json:"backfill_requests_per_channel"`
	BackfillMBChan   float64      `json:"backfill_mb_per_channel"`
	PeakHour         time.Time    `json:"peak_hour"`
	PeakHourRequests int64        `json:"peak_hour_requests"` // steady state
	AvgHourRequests  float64      `json:"avg_hour_requests"`  // steady state
	DB               *DBGrowth    `json:"database,omitempty"`
	Quality          Quality      `json:"quality"`
	Projections      []Projection `json:"projections"`
	Notes            []string     `json:"notes"`
}

// Build reads the recorded usage for the window and projects it.
func Build(ctx context.Context, q db.Querier, o Options) (Report, error) {
	if !o.To.After(o.From) {
		return Report{}, fmt.Errorf("report: the window ends (%s) before it starts (%s)", o.To.Format(time.RFC3339), o.From.Format(time.RFC3339))
	}
	if o.Targets == nil {
		o.Targets = []int{1000, 10000}
	}
	r := Report{From: o.From, To: o.To}
	if err := r.kinds(ctx, q); err != nil {
		return r, err
	}
	if err := r.gap(ctx, q, o.Gap); err != nil {
		return r, err
	}
	if err := r.channels(ctx, q); err != nil {
		return r, err
	}
	if err := r.database(ctx, q); err != nil {
		return r, err
	}
	if err := r.quality(ctx, q); err != nil {
		return r, err
	}
	r.project(o.Targets)
	return r, nil
}

func (r *Report) kinds(ctx context.Context, q db.Querier) error {
	rows, err := q.Query(ctx, `SELECT kind, sum(jobs), sum(failures), sum(requests), sum(bytes), sum(blocks), sum(run_ms)
		FROM usage_hourly WHERE hour >= date_trunc('hour', $1::timestamptz) AND hour < $2 GROUP BY kind ORDER BY sum(requests) DESC, kind`,
		r.From, r.To)
	if err != nil {
		return err
	}
	groups := map[string]*KindCost{}
	for rows.Next() {
		var k KindCost
		if err := rows.Scan(&k.Kind, &k.Jobs, &k.Failures, &k.Requests, &k.Bytes, &k.Blocks, &k.RunMS); err != nil {
			rows.Close()
			return err
		}
		k.Group = Group(k.Kind)
		r.Kinds = append(r.Kinds, k)
		r.Total.add(k.Cost)
		g := groups[k.Group]
		if g == nil {
			g = &KindCost{Kind: k.Group, Group: k.Group}
			groups[k.Group] = g
		}
		g.add(k.Cost)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range []string{GroupChannel, GroupFollows, GroupDiscovery, GroupBackfill} {
		if g := groups[name]; g != nil {
			r.Groups = append(r.Groups, *g)
		} else {
			r.Groups = append(r.Groups, KindCost{Kind: name, Group: name})
		}
	}
	// The hours collection ran, and the busiest one in steady state.
	var first, last *time.Time
	if err := q.QueryRow(ctx, `SELECT min(hour), max(hour) FROM usage_hourly
		WHERE hour >= date_trunc('hour', $1::timestamptz) AND hour < $2`, r.From, r.To).Scan(&first, &last); err != nil {
		return err
	}
	if first == nil {
		r.Notes = append(r.Notes, "No jobs were recorded in this window.")
		return nil
	}
	r.DataFrom, r.DataTo = *first, last.Add(time.Hour)
	r.Hours = r.DataTo.Sub(r.DataFrom).Hours()
	var peak *time.Time
	var peakReq *int64
	if err := q.QueryRow(ctx, `SELECT hour, sum(requests)::bigint FROM usage_hourly
		WHERE hour >= date_trunc('hour', $1::timestamptz) AND hour < $2 AND kind <> $3
		GROUP BY hour ORDER BY 2 DESC, 1 LIMIT 1`, r.From, r.To, queue.KindBackfill).Scan(&peak, &peakReq); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if peak != nil {
		r.PeakHour, r.PeakHourRequests = *peak, *peakReq
	}
	r.AvgHourRequests = float64(r.Total.Requests-r.group(GroupBackfill).Requests) / r.Hours
	return nil
}

func (r *Report) group(name string) Cost {
	for _, g := range r.Groups {
		if g.Kind == name {
			return g.Cost
		}
	}
	return Cost{}
}

// gap takes the per-IP gap and the backfill depth from the latest worker
// announcement, whatever its age (the pilot may have ended).
func (r *Report) gap(ctx context.Context, q db.Querier, override time.Duration) error {
	var w struct {
		IntervalS    float64   `json:"interval_s"`
		BackfillDays int       `json:"backfill_days"`
		At           time.Time `json:"at"`
	}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT value FROM settings WHERE key LIKE 'capacity/%' ORDER BY updated_at DESC LIMIT 1`).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &w)
	}
	r.BackfillDepth = w.BackfillDays
	switch {
	case override > 0:
		r.Gap, r.GapSource = override.String(), "set with -gap"
	case w.IntervalS > 0:
		r.Gap, r.GapSource = time.Duration(w.IntervalS*float64(time.Second)).String(), "as the workers announced it"
	default:
		r.Gap, r.GapSource = DefaultGap.String(), "no worker announced one; the plan's default"
	}
	return nil
}

func (r *Report) gapDuration() time.Duration {
	d, _ := time.ParseDuration(r.Gap)
	if d <= 0 {
		d = DefaultGap
	}
	return d
}

// channels reads the per-channel table: the Iraq days the window touches,
// divided by the hours collection ran on those days.
func (r *Report) channels(ctx context.Context, q db.Querier) error {
	fromDay, toDay := Day(r.From), Day(r.To.Add(-time.Nanosecond))
	dayStart, _ := time.ParseInLocation(time.DateOnly, fromDay, Iraq)
	dayEnd, _ := time.ParseInLocation(time.DateOnly, toDay, Iraq)
	dayEnd = dayEnd.Add(24 * time.Hour)
	for d := dayStart; d.Before(dayEnd); d = d.Add(24 * time.Hour) {
		r.ChannelDays = append(r.ChannelDays, d.Format(time.DateOnly))
	}
	var first, last *time.Time
	if err := q.QueryRow(ctx, `SELECT min(hour), max(hour) FROM usage_hourly WHERE hour >= $1 AND hour < $2`,
		dayStart, dayEnd).Scan(&first, &last); err != nil {
		return err
	}
	if first == nil {
		return nil
	}
	r.ChannelHours = last.Add(time.Hour).Sub(*first).Hours()
	days := r.ChannelHours / 24
	rows, err := q.Query(ctx, `SELECT u.channel_id, COALESCE(c.tier, '-'), sum(u.requests - u.backfill_requests),
			sum(u.bytes - u.backfill_bytes), sum(u.backfill_requests), sum(u.backfill_bytes)
		FROM usage_channel_daily u LEFT JOIN channels c ON c.id = u.channel_id
		WHERE u.day >= $1::date AND u.day <= $2::date GROUP BY 1, 2`, fromDay, toDay)
	if err != nil {
		return err
	}
	byTier := map[string]*[2][]float64{}
	all := &[2][]float64{}
	var backfill []float64
	var backfillBytes float64
	for rows.Next() {
		var id, tier string
		var req, bytes, bfReq, bfBytes int64
		if err := rows.Scan(&id, &tier, &req, &bytes, &bfReq, &bfBytes); err != nil {
			rows.Close()
			return err
		}
		r.Channels++
		t := byTier[tier]
		if t == nil {
			t = &[2][]float64{}
			byTier[tier] = t
		}
		perDayReq, perDayMB := float64(req)/days, float64(bytes)/days/1e6
		t[0], t[1] = append(t[0], perDayReq), append(t[1], perDayMB)
		all[0], all[1] = append(all[0], perDayReq), append(all[1], perDayMB)
		if bfReq > 0 {
			backfill = append(backfill, float64(bfReq))
			backfillBytes += float64(bfBytes)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tiers := make([]string, 0, len(byTier))
	for t := range byTier {
		tiers = append(tiers, t)
	}
	sort.Strings(tiers)
	for _, t := range tiers {
		v := byTier[t]
		r.Tiers = append(r.Tiers, TierCost{Tier: t, Channels: len(v[0]), Requests: spread(v[0]), MB: spread(v[1])})
	}
	if r.Channels > 0 {
		r.Tiers = append(r.Tiers, TierCost{Tier: "all", Channels: r.Channels, Requests: spread(all[0]), MB: spread(all[1])})
	}
	r.BackfillChannels = len(backfill)
	if len(backfill) > 0 {
		r.BackfillPerChan = spread(backfill)
		r.BackfillMBChan = backfillBytes / float64(len(backfill)) / 1e6
	}
	return nil
}

func (r *Report) database(ctx context.Context, q db.Querier) error {
	type sample struct {
		at     time.Time
		bytes  int64
		tables map[string]int64
	}
	read := func(order string) (*sample, error) {
		var s sample
		var raw []byte
		err := q.QueryRow(ctx, `SELECT at, bytes, tables FROM db_sizes WHERE at >= $1 AND at <= $2 ORDER BY at `+order+` LIMIT 1`,
			r.From, r.To).Scan(&s.at, &s.bytes, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &s.tables)
		return &s, nil
	}
	first, err := read("ASC")
	if err != nil || first == nil {
		return err
	}
	last, err := read("DESC")
	if err != nil {
		return err
	}
	g := &DBGrowth{From: first.at, To: last.at, FromBytes: first.bytes, ToBytes: last.bytes}
	if h := last.at.Sub(first.at).Hours(); h >= 1 {
		g.PerDay = float64(last.bytes-first.bytes) / h * 24
	}
	for t, n := range last.tables {
		g.Tables = append(g.Tables, TableGrowth{Table: t, Bytes: n - first.tables[t]})
	}
	sort.Slice(g.Tables, func(i, j int) bool {
		if g.Tables[i].Bytes != g.Tables[j].Bytes {
			return g.Tables[i].Bytes > g.Tables[j].Bytes
		}
		return g.Tables[i].Table < g.Tables[j].Table
	})
	if len(g.Tables) > 10 {
		g.Tables = g.Tables[:10]
	}
	r.DB = g
	return nil
}

func (r *Report) quality(ctx context.Context, q db.Querier) error {
	qa := &r.Quality
	if err := q.QueryRow(ctx, `SELECT (SELECT count(*) FROM videos WHERE first_seen >= $1 AND first_seen < $2),
			(SELECT count(*) FROM comments WHERE first_seen >= $1 AND first_seen < $2)`, r.From, r.To).
		Scan(&qa.NewVideos, &qa.NewComments); err != nil {
		return err
	}
	if err := q.QueryRow(ctx, `SELECT count(*), percentile_cont(0.5) WITHIN GROUP (ORDER BY comments_complete_pct),
			avg(comments_complete_pct)
		FROM videos WHERE comments_complete_pct IS NOT NULL AND last_checked >= $1 AND last_checked < $2`, r.From, r.To).
		Scan(&qa.CompletenessVideos, &qa.CompletenessMedian, &qa.CompletenessMean); err != nil {
		return err
	}
	// Detection: uploads published in the window with an exact time.
	const detected = `FROM videos WHERE published_at >= $1 AND published_at < $2 AND published_precision_s = 0
		AND first_seen >= published_at AND first_seen < $2`
	if err := q.QueryRow(ctx, `SELECT count(*),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM first_seen - published_at) / 60),
			percentile_cont(0.9) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM first_seen - published_at) / 60) `+detected,
		r.From, r.To).Scan(&qa.DetectionVideos, &qa.DetectionMedianMin, &qa.DetectionP90Min); err != nil {
		return err
	}
	rows, err := q.Query(ctx, `SELECT source, count(*),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM first_seen - published_at) / 60) `+detected+`
		GROUP BY source ORDER BY source`, r.From, r.To)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s SourceTime
		if err := rows.Scan(&s.Source, &s.Videos, &s.MedianMin); err != nil {
			rows.Close()
			return err
		}
		qa.DetectionBySourceMin = append(qa.DetectionBySourceMin, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	qa.Dead, err = queue.DeadJobs(ctx, q)
	return err
}

// ipsFor is the IPs a peak hour needs: at most TargetUse busy, plus Spare.
func ipsFor(peakHour float64, gap time.Duration) int {
	perIP := 3600 / gap.Seconds() * TargetUse
	n := int(math.Ceil(peakHour / perIP * (1 + Spare)))
	return max(n, MinIPs)
}

func (r *Report) project(targets []int) {
	if r.Channels == 0 || r.Hours <= 0 {
		r.Notes = append(r.Notes, "No monitored channel had jobs in this window, so there are no projections.")
		return
	}
	gap := r.gapDuration()
	perDay := func(c Cost) (req, bytes float64) {
		return float64(c.Requests) / r.Hours * 24, float64(c.Bytes) / r.Hours * 24
	}
	chReq, chBytes := perDay(r.group(GroupChannel))
	foReq, foBytes := perDay(r.group(GroupFollows))
	diReq, diBytes := perDay(r.group(GroupDiscovery))
	steadyHour := (chReq + foReq + diReq) / 24
	peakRatio := 1.0
	if steadyHour > 0 && r.PeakHourRequests > 0 {
		peakRatio = math.Max(1, float64(r.PeakHourRequests)/steadyHour)
	}
	bf := r.group(GroupBackfill)
	for _, n := range append([]int{r.Channels}, targets...) {
		scale := float64(n) / float64(r.Channels)
		p := Projection{Channels: n, ChannelRequests: chReq * scale, FollowsRequests: foReq * scale, DiscoveryRequests: diReq}
		p.RequestsPerDay = p.ChannelRequests + p.FollowsRequests + p.DiscoveryRequests
		bytes := (chBytes+foBytes)*scale + diBytes
		p.GBPerDay = bytes / 1e9
		p.GBPerMonth = p.GBPerDay * 30
		p.PeakHourRequests = p.RequestsPerDay / 24 * peakRatio
		p.IPs = ipsFor(p.PeakHourRequests, gap)
		if r.BackfillChannels > 0 {
			p.BackfillRequests = r.BackfillPerChan.Mean * float64(n)
			p.BackfillGB = float64(bf.Bytes) / float64(r.BackfillChannels) * float64(n) / 1e9
			perIPWindow := float64(BackfillDays) * 86400 / gap.Seconds() * TargetUse
			p.BackfillIPs = int(math.Ceil(p.BackfillRequests / perIPWindow * (1 + Spare)))
			if r.BackfillDepth > 0 && r.BackfillDepth < 365 {
				p.FullBackfill = p.BackfillRequests / float64(r.BackfillDepth) * 365
			}
		}
		if r.DB != nil && r.DB.PerDay > 0 {
			p.DBGBPerYear = r.DB.PerDay * 365 * scale / 1e9
		}
		r.Projections = append(r.Projections, p)
	}
	r.Notes = append(r.Notes,
		"Projections assume the window's mix of tiers and channel sizes. Channel work, enrichment and images grow with the channel count; discovery doesn't (it has no caps and grows with the search terms), so it is shown as measured.",
		fmt.Sprintf("IPs: the busiest steady hour, each IP at most %.0f%% busy at a %s gap, plus %.0f%% spare for failover and rests, and never fewer than %d.",
			TargetUse*100, gap, Spare*100, MinIPs),
		fmt.Sprintf("The backfill is once per new channel; its extra IPs finish it within %d days.", BackfillDays))
	if r.BackfillDepth > 0 && r.BackfillDepth < 365 {
		r.Notes = append(r.Notes, fmt.Sprintf("The backfill read %d days of uploads; the 365-day figure scales it in proportion, an estimate (older uploads have fewer new comments).", r.BackfillDepth))
	}
	if r.DB != nil {
		r.Notes = append(r.Notes, "Database growth includes the backfill's writes, so it overstates steady growth while backfills run.")
	}
}

func spread(v []float64) Spread {
	if len(v) == 0 {
		return Spread{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	return Spread{Mean: sum / float64(len(s)), Median: pct(s, 50), P90: pct(s, 90), Max: s[len(s)-1]}
}

// pct is the nearest-rank percentile of sorted values.
func pct(s []float64, p float64) float64 {
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// Markdown renders the report for people: plain language, Iraq times.
func (r Report) Markdown() string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	t := func(x time.Time) string { return x.In(Iraq).Format("2 Jan 2006 15:04") }
	p("# yts usage report")
	p("")
	p("Window: %s to %s, Iraq time.", t(r.From), t(r.To))
	if r.Hours == 0 {
		p("")
		for _, n := range r.Notes {
			p("%s", n)
		}
		return b.String()
	}
	p("Jobs ran from %s to %s: %s hours. Per-IP gap: %s (%s).", t(r.DataFrom), t(r.DataTo), num(r.Hours, 1), r.Gap, r.GapSource)
	p("")
	p("## Per channel per day (steady state, without the backfill)")
	p("")
	days := r.ChannelDays[0]
	if len(r.ChannelDays) > 1 {
		days = fmt.Sprintf("%s to %s (%d days)", r.ChannelDays[0], r.ChannelDays[len(r.ChannelDays)-1], len(r.ChannelDays))
	}
	p("%d monitored channels had jobs. Days counted, Iraq time: %s, with jobs for %s hours.", r.Channels, days, num(r.ChannelHours, 1))
	p("")
	p("| Tier | Channels | Requests: mean | median | 90th percentile | most | MB: mean | median | 90th percentile |")
	p("|---|---|---|---|---|---|---|---|---|")
	for _, x := range r.Tiers {
		p("| %s | %d | %s | %s | %s | %s | %s | %s | %s |", x.Tier, x.Channels, num(x.Requests.Mean, 0), num(x.Requests.Median, 0),
			num(x.Requests.P90, 0), num(x.Requests.Max, 0), num(x.MB.Mean, 1), num(x.MB.Median, 1), num(x.MB.P90, 1))
	}
	if r.BackfillChannels > 0 {
		p("")
		depth := "the configured depth"
		if r.BackfillDepth > 0 {
			depth = fmt.Sprintf("%d days", r.BackfillDepth)
		}
		p("**Backfill (once per channel, %s):** %d channels; requests per channel mean %s, median %s, most %s; %s MB per channel.",
			depth, r.BackfillChannels, num(r.BackfillPerChan.Mean, 0), num(r.BackfillPerChan.Median, 0), num(r.BackfillPerChan.Max, 0), num(r.BackfillMBChan, 1))
	}
	p("")
	p("## In total")
	p("")
	p("| Job kind | Group | Jobs | Failed | Requests | Per day | GB | Blocks per 1,000 requests | Run time (hours) |")
	p("|---|---|---|---|---|---|---|---|---|")
	row := func(name, group string, c Cost) {
		p("| %s | %s | %s | %s | %s | %s | %s | %s | %s |", name, group, num(float64(c.Jobs), 0), num(float64(c.Failures), 0),
			num(float64(c.Requests), 0), num(float64(c.Requests)/r.Hours*24, 0), num(float64(c.Bytes)/1e9, 2),
			num(per1000(c.Blocks, c.Requests), 1), num(float64(c.RunMS)/3.6e6, 1))
	}
	for _, k := range r.Kinds {
		row(k.Kind, k.Group, k.Cost)
	}
	row("**all**", "", r.Total)
	p("")
	if r.PeakHourRequests > 0 {
		p("Steady state: %s requests an hour on average; the busiest hour was %s with %s.", num(r.AvgHourRequests, 0),
			t(r.PeakHour), num(float64(r.PeakHourRequests), 0))
	}
	p("")
	p("## Projected")
	p("")
	if len(r.Projections) > 0 {
		p("| Channels | Requests a day | of which channel work | enrichment and images | discovery | GB a day | GB a month | Busiest hour | IPs | Backfill if all are new (requests, once) | Backfill GB | Extra IPs for the backfill | Full 365-day backfill (estimate) | Database GB a year |")
		p("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
		for i, x := range r.Projections {
			name := num(float64(x.Channels), 0)
			if i == 0 {
				name += " (measured)"
			}
			full, dbYear := "—", "—"
			if x.FullBackfill > 0 {
				full = num(x.FullBackfill, 0)
			}
			if x.DBGBPerYear > 0 {
				dbYear = num(x.DBGBPerYear, 1)
			}
			p("| %s | %s | %s | %s | %s | %s | %s | %s | %d | %s | %s | %d | %s | %s |", name, num(x.RequestsPerDay, 0),
				num(x.ChannelRequests, 0), num(x.FollowsRequests, 0), num(x.DiscoveryRequests, 0), num(x.GBPerDay, 2),
				num(x.GBPerMonth, 1), num(x.PeakHourRequests, 0), x.IPs, num(x.BackfillRequests, 0), num(x.BackfillGB, 2),
				x.BackfillIPs, full, dbYear)
		}
		p("")
	}
	if r.DB != nil {
		p("## Database")
		p("")
		p("%s GB at %s, %s GB at %s: %s MB a day.", num(float64(r.DB.FromBytes)/1e9, 2), t(r.DB.From),
			num(float64(r.DB.ToBytes)/1e9, 2), t(r.DB.To), num(r.DB.PerDay/1e6, 1))
		p("")
		p("| Table | Growth (MB, with indexes) |")
		p("|---|---|")
		for _, x := range r.DB.Tables {
			p("| %s | %s |", x.Table, num(float64(x.Bytes)/1e6, 1))
		}
		p("")
	}
	qa := r.Quality
	p("## Quality")
	p("")
	p("- New videos: %s. New comments: %s.", num(float64(qa.NewVideos), 0), num(float64(qa.NewComments), 0))
	if qa.CompletenessMedian != nil {
		p("- Comment completeness (collected ÷ shown, %s videos checked): median %s%%, mean %s%%.",
			num(float64(qa.CompletenessVideos), 0), num(*qa.CompletenessMedian, 1), num(*qa.CompletenessMean, 1))
	}
	if qa.DetectionMedianMin != nil {
		p("- Detection delay (uploads published in the window with an exact time, %s videos): median %s minutes, 90th percentile %s.",
			num(float64(qa.DetectionVideos), 0), num(*qa.DetectionMedianMin, 0), num(*qa.DetectionP90Min, 0))
		for _, s := range qa.DetectionBySourceMin {
			p("  - found by %s: %s videos, median %s minutes", s.Source, num(float64(s.Videos), 0), num(s.MedianMin, 0))
		}
	}
	p("- Blocks: %s per 1,000 requests. Failed jobs: %s%%.", num(per1000(r.Total.Blocks, r.Total.Requests), 1),
		num(per1000(r.Total.Failures, r.Total.Jobs)/10, 1))
	if len(qa.Dead) == 0 {
		p("- Dead jobs now: none.")
	} else {
		var n int64
		for _, d := range qa.Dead {
			n += d.Count
		}
		p("- Dead jobs now: %d (\"ytcollect jobs dead\" lists them).", n)
	}
	p("- Memory and CPU: from the cluster's metrics (process_resident_memory_bytes, process_cpu_seconds_total).")
	p("")
	p("## Notes")
	p("")
	for _, n := range r.Notes {
		p("- %s", n)
	}
	return b.String()
}

func per1000(n, of int64) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) * 1000 / float64(of)
}

// num formats with thousands separators.
func num(x float64, decimals int) string {
	s := fmt.Sprintf("%.*f", decimals, x)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var out []byte
	for i := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, whole[i])
	}
	res := string(out)
	if frac != "" {
		res += "." + frac
	}
	if neg {
		res = "-" + res
	}
	return res
}
