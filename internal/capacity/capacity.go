// Package capacity is the capacity planner (plan §6.6, §8.2): every hour it
// compares what the proxy pool can do (supply) with what the schedule asks
// for (demand). When demand is higher, the refresh intervals of tiers C, D
// and M are stretched (A and B never are, nor is new-upload detection), and
// it reports the freshness debt per tier and how many IPs would close the
// gap.
//
// Supply is what the IPs make at the plan's 70% busy (usage.TargetUse),
// counting each answer's time on top of the gap (usage.PerIPHour). Demand
// splits into work the stretch can't slow (new uploads, the feeds and tabs,
// tiers A and B, backfill, discovery, enrichment and images) and the
// refreshes it can (C, D and M channels' snapshots, posts, playlists, video
// stats and comment scans). The stretch is what makes them fit, moved at
// most 1.5 times up or down per run, and never below Options.MinStretch
// (the owner may slow refreshes on purpose). The lowest-priority work (P4:
// images and commenter enrichment) is left out: it runs in whatever time is
// left and never slows a refresh (SpareTimePerHour reports it).
package capacity

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/usage"
)

// StretchKey is the settings key workers read the stretch from.
const StretchKey = "capacity_stretch"

// MaxStretch caps how far C, D and M intervals are stretched.
const MaxStretch = 8.0

// maxStep is how far one run moves the stretch, up or down: the schedule
// takes a while to follow a new stretch, so the estimate lags behind it.
const maxStep = 1.5

// minSample is the finished jobs of a kind in the last day from which its
// measured cost replaces the estimate in Cost.
const minSample = 20

// Cost is the estimated YouTube requests per run of each job kind, used
// until a kind has minSample finished jobs in the last day.
var Cost = map[string]float64{
	// RSS: 1 request, or up to 3 tab pages while the feed service is down
	// (Phase 0: about 40% of the time).
	queue.KindResolve: 2, queue.KindChannel: 2, queue.KindTabs: 4, queue.KindBackfill: 40, queue.KindRSS: 2,
	queue.KindPosts: 2, queue.KindPlaylists: 4, queue.KindVideo: 2, queue.KindComments: 4, queue.KindSweep: 15,
	queue.KindPostComments: 3, queue.KindEnrich: 2, queue.KindCandidate: 3, queue.KindSearch: 4,
	queue.KindLive: 1, queue.KindWebSub: 0, "erasure": 0,
}

// Stretched are the job kinds whose next run the stretch delays, for tiers
// C, D and M (policy.Stretch); a video's first read (P0) never is.
var Stretched = []string{queue.KindChannel, queue.KindPosts, queue.KindPlaylists, queue.KindVideo,
	queue.KindComments, queue.KindPostComments}

// Worker is what a worker process announces about itself.
type Worker struct {
	Proxies      int       `json:"proxies"`
	IntervalS    float64   `json:"interval_s"`
	BackfillDays int       `json:"backfill_days,omitempty"` // for "ytcollect report"
	At           time.Time `json:"at"`
	// Stopped is set when the worker shuts down, so the planner stops
	// counting it at once (a new pod announces itself while the old one's
	// announcement is still fresh). The report still reads the rest.
	Stopped time.Time `json:"stopped,omitempty"`
}

// Announce records a worker's capacity (workers call it every few minutes).
func Announce(ctx context.Context, q db.Querier, owner string, w Worker) error {
	return db.SetSetting(ctx, q, "capacity/"+owner, w)
}

// Withdraw marks a worker's announcement stopped (at shutdown).
func Withdraw(ctx context.Context, q db.Querier, owner string, w Worker, at time.Time) error {
	w.Stopped = at
	return Announce(ctx, q, owner, w)
}

// Options tune a planner run.
type Options struct {
	// MinStretch is the least stretch for C, D and M; 1 or less: none.
	MinStretch float64
}

// Report is one planner run.
type Report struct {
	At            time.Time `json:"at"`
	SupplyPerHour float64   `json:"supply_per_hour"` // at usage.TargetUse
	// DemandPerHour is the work the schedule asks for without any stretch:
	// FixedPerHour, which the stretch can't slow, plus StretchablePerHour.
	DemandPerHour      float64 `json:"demand_per_hour"`
	FixedPerHour       float64 `json:"fixed_per_hour"`
	StretchablePerHour float64 `json:"stretchable_per_hour"`
	SpareTimePerHour   float64 `json:"spare_time_per_hour"` // P4 work, not in the demand
	// DemandByKind splits the demand (fixed and stretchable, without any
	// stretch) by job kind, in requests per hour.
	DemandByKind   map[string]float64 `json:"demand_by_kind,omitempty"`
	Stretch        float64            `json:"stretch"`
	MinStretch     float64            `json:"min_stretch,omitempty"`
	DebtHours      map[string]float64 `json:"debt_hours"`       // overdue job-hours per tier, P4 work left out
	SpareDebtHours float64            `json:"spare_debt_hours"` // overdue job-hours of P4 work (images, enrichment)
	RecommendedIPs int                `json:"recommended_ips"`  // to do it all without a stretch
	Workers        int                `json:"workers"`
	Measured       map[string]float64 `json:"measured_cost,omitempty"` // requests per job, last day
}

// PerIPHour is what one IP makes in an hour at a gap of intervalS seconds,
// at the plan's 70% busy.
func PerIPHour(intervalS float64) float64 {
	return usage.PerIPHour(time.Duration(intervalS*float64(time.Second))) * usage.TargetUse
}

// Plan computes a report, stores it, and sets the stretch.
func Plan(ctx context.Context, q db.Querier, now time.Time) (Report, error) {
	return PlanWith(ctx, q, now, Options{})
}

// PlanWith is Plan with options.
func PlanWith(ctx context.Context, q db.Querier, now time.Time, o Options) (Report, error) {
	r := Report{At: now, DebtHours: map[string]float64{}, Stretch: 1, Measured: map[string]float64{}, DemandByKind: map[string]float64{}}
	if o.MinStretch > 1 {
		r.MinStretch = math.Min(o.MinStretch, MaxStretch)
	}
	// Supply: live workers (announced within 15 minutes).
	rows, err := q.Query(ctx, `SELECT value FROM settings WHERE key LIKE 'capacity/%'`)
	if err != nil {
		return r, err
	}
	var interval float64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return r, err
		}
		var w Worker
		if json.Unmarshal(raw, &w) != nil || now.Sub(w.At) > 15*time.Minute || w.IntervalS <= 0 || !w.Stopped.IsZero() {
			continue
		}
		r.Workers++
		r.SupplyPerHour += float64(w.Proxies) * PerIPHour(w.IntervalS)
		interval = math.Max(interval, w.IntervalS)
	}
	rows.Close()
	// What a job of each kind cost in the last day, where enough finished.
	cost := map[string]float64{}
	for k, v := range Cost {
		cost[k] = v
	}
	rows, err = q.Query(ctx, `SELECT kind, sum(requests)::float8, sum(jobs - failures)::bigint FROM usage_hourly
		WHERE hour >= $1::timestamptz - interval '24 hours' GROUP BY kind`, now)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var kind string
		var requests float64
		var done int64
		if err := rows.Scan(&kind, &requests, &done); err != nil {
			rows.Close()
			return r, err
		}
		if done >= minSample {
			cost[kind] = requests / float64(done)
			r.Measured[kind] = math.Round(cost[kind]*10) / 10
		}
	}
	rows.Close()
	// The stretch in force: the stretched jobs due in the next day are fewer
	// by that much, so their count times it is what they would be without.
	current := 1.0
	if ok, err := db.GetSetting(ctx, q, StretchKey, &current); err != nil {
		return r, err
	} else if !ok || current < 1 {
		current = 1
	}
	// Demand: the next 24 hours of jobs, overdue ones included.
	rows, err = q.Query(ctx, `SELECT s.kind,
			COALESCE(c.tier, '') IN ('C', 'D', 'M') AND s.kind = ANY($2) AND s.priority > $3 AS stretched,
			s.priority >= $4 AS spare,
			count(*) FILTER (WHERE s.due_at <= $1::timestamptz + interval '24 hours')
		FROM schedule s LEFT JOIN channels c ON c.id = s.channel_id
		WHERE s.dead_at IS NULL GROUP BY 1, 2, 3`, now, Stretched, queue.P0, queue.P4)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var kind string
		var stretched, spare bool
		var next24 int64
		if err := rows.Scan(&kind, &stretched, &spare, &next24); err != nil {
			rows.Close()
			return r, err
		}
		c, ok := cost[kind]
		if !ok {
			c = 2
		}
		switch {
		case spare:
			r.SpareTimePerHour += float64(next24) * c / 24
		case stretched:
			r.StretchablePerHour += float64(next24) * c * current / 24
			r.DemandByKind[kind] += float64(next24) * c * current / 24
		default:
			r.FixedPerHour += float64(next24) * c / 24
			r.DemandByKind[kind] += float64(next24) * c / 24
		}
	}
	rows.Close()
	r.DemandPerHour = r.FixedPerHour + r.StretchablePerHour
	// Freshness debt: how far behind the overdue jobs are, per tier. P4 work
	// runs in spare time, so its backlog is reported apart; "-" is work of
	// no listed channel (discovery, and videos of unlisted channels).
	rows, err = q.Query(ctx, `SELECT CASE WHEN s.priority >= $2 THEN 'spare' ELSE COALESCE(c.tier, '-') END,
			sum(EXTRACT(EPOCH FROM $1::timestamptz - s.due_at) / 3600)
		FROM schedule s LEFT JOIN channels c ON c.id = s.channel_id
		WHERE s.due_at < $1::timestamptz - interval '15 minutes' AND s.dead_at IS NULL GROUP BY 1`, now, queue.P4)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var tier string
		var h float64
		if err := rows.Scan(&tier, &h); err != nil {
			rows.Close()
			return r, err
		}
		if tier == "spare" {
			r.SpareDebtHours = math.Round(h*10) / 10
			continue
		}
		r.DebtHours[tier] = math.Round(h*10) / 10
	}
	rows.Close()
	r.Stretch = stretchFor(r.SupplyPerHour, r.FixedPerHour, r.StretchablePerHour, current, r.MinStretch)
	if interval > 0 {
		r.RecommendedIPs = int(math.Ceil(r.DemandPerHour / PerIPHour(interval)))
	}
	detail, _ := json.Marshal(r)
	debt, _ := json.Marshal(r.DebtHours)
	if _, err := q.Exec(ctx, `INSERT INTO capacity_reports (at, supply_per_hour, demand_per_hour, stretch, debt, recommended_ips, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (at) DO NOTHING`,
		now, r.SupplyPerHour, r.DemandPerHour, r.Stretch, debt, r.RecommendedIPs, detail); err != nil {
		return r, err
	}
	return r, db.SetSetting(ctx, q, StretchKey, r.Stretch)
}

// TopKinds names the job kinds with the most demand, biggest first, as
// "kind=requests/h" (for the log).
func (r Report) TopKinds(n int) string {
	type kv struct {
		k string
		v float64
	}
	var all []kv
	for k, v := range r.DemandByKind {
		if v >= 0.5 {
			all = append(all, kv{k, v})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v || (all[i].v == all[j].v && all[i].k < all[j].k) })
	var parts []string
	for i, x := range all {
		if i == n {
			break
		}
		parts = append(parts, fmt.Sprintf("%s=%.0f", x.k, x.v))
	}
	return strings.Join(parts, " ")
}

// stretchFor is the stretch that fits fixed + stretchable/stretch into
// supply, moved at most maxStep from current, at least floor, at most
// MaxStretch. With no live worker (no supply) the stretch stays as it is.
func stretchFor(supply, fixed, stretchable, current, floor float64) float64 {
	want := 1.0
	switch {
	case supply <= 0:
		want = current
	case stretchable <= 0:
	case fixed >= supply:
		want = MaxStretch
	default:
		want = stretchable / (supply - fixed)
	}
	want = math.Max(current/maxStep, math.Min(current*maxStep, want))
	want = math.Max(want, floor)
	want = math.Max(1, math.Min(MaxStretch, want))
	return math.Round(want*100) / 100
}

// Stretcher gives workers the current stretch, re-read every 5 minutes.
type Stretcher struct {
	Q  db.Querier
	mu sync.Mutex
	v  float64
	at time.Time
}

// Value is the stretch for C, D and M (1 = none).
func (s *Stretcher) Value() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.at) > 5*time.Minute {
		var v float64
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ok, err := db.GetSetting(ctx, s.Q, StretchKey, &v)
		cancel()
		if err == nil {
			s.v, s.at = 1, time.Now()
			if ok && v >= 1 {
				s.v = v
			}
		}
	}
	if s.v < 1 {
		return 1
	}
	return s.v
}

func (r Report) String() string {
	return fmt.Sprintf("supply %.0f/h, demand %.0f/h (fixed %.0f, stretchable %.0f; spare-time work %.0f), stretch %.2f, recommended IPs %d, debt %v",
		r.SupplyPerHour, r.DemandPerHour, r.FixedPerHour, r.StretchablePerHour, r.SpareTimePerHour, r.Stretch, r.RecommendedIPs, r.DebtHours)
}
