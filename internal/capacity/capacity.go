// Package capacity is the capacity planner (plan §6.6, §8.2): every hour it
// compares what the proxy pool can do (supply) with what the schedule asks
// for (demand). When demand is higher, the intervals of tiers C, D and M
// are stretched (A and B never are), and it reports the freshness debt per
// tier and how many IPs would close the gap.
package capacity

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

// StretchKey is the settings key workers read the stretch from.
const StretchKey = "capacity_stretch"

// MaxStretch caps how far C, D and M intervals are stretched.
const MaxStretch = 8.0

// Efficiency is the share of a proxy's raw pace that real work achieves
// (retries, waits for a channel's previous job, failovers).
const Efficiency = 0.8

// Cost is the estimated YouTube requests per run of each job kind.
var Cost = map[string]float64{
	queue.KindResolve: 2, queue.KindChannel: 2, queue.KindTabs: 4, queue.KindBackfill: 40, queue.KindRSS: 1,
	queue.KindPosts: 2, queue.KindPlaylists: 4, queue.KindVideo: 2, queue.KindComments: 4, queue.KindSweep: 15,
	queue.KindPostComments: 3, queue.KindEnrich: 2, queue.KindCandidate: 3, queue.KindSearch: 4,
	queue.KindLive: 1, queue.KindWebSub: 0, "erasure": 0,
}

// Worker is what a worker process announces about itself.
type Worker struct {
	Proxies   int       `json:"proxies"`
	IntervalS float64   `json:"interval_s"`
	At        time.Time `json:"at"`
}

// Announce records a worker's capacity (workers call it every few minutes).
func Announce(ctx context.Context, q db.Querier, owner string, w Worker) error {
	return db.SetSetting(ctx, q, "capacity/"+owner, w)
}

// Report is one planner run.
type Report struct {
	At             time.Time          `json:"at"`
	SupplyPerHour  float64            `json:"supply_per_hour"`
	DemandPerHour  float64            `json:"demand_per_hour"`
	Stretch        float64            `json:"stretch"`
	DebtHours      map[string]float64 `json:"debt_hours"` // overdue job-hours per tier
	RecommendedIPs int                `json:"recommended_ips"`
	Workers        int                `json:"workers"`
}

// Plan computes a report, stores it, and sets the stretch.
func Plan(ctx context.Context, q db.Querier, now time.Time) (Report, error) {
	r := Report{At: now, DebtHours: map[string]float64{}, Stretch: 1}
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
		if json.Unmarshal(raw, &w) != nil || now.Sub(w.At) > 15*time.Minute || w.IntervalS <= 0 {
			continue
		}
		r.Workers++
		r.SupplyPerHour += float64(w.Proxies) * 3600 / w.IntervalS * Efficiency
		interval = math.Max(interval, w.IntervalS)
	}
	rows.Close()
	// Demand: the next 24 hours of jobs, plus clearing today's backlog.
	rows, err = q.Query(ctx, `SELECT kind, count(*) FILTER (WHERE due_at <= $1::timestamptz + interval '24 hours'),
			count(*) FILTER (WHERE due_at <= $1::timestamptz) FROM schedule GROUP BY kind`, now)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var kind string
		var next24, overdue int64
		if err := rows.Scan(&kind, &next24, &overdue); err != nil {
			rows.Close()
			return r, err
		}
		c, ok := Cost[kind]
		if !ok {
			c = 2
		}
		r.DemandPerHour += float64(next24) * c / 24
	}
	rows.Close()
	// Freshness debt: how far behind the overdue jobs are, per tier.
	rows, err = q.Query(ctx, `SELECT COALESCE(c.tier, '-'), sum(EXTRACT(EPOCH FROM $1::timestamptz - s.due_at) / 3600)
		FROM schedule s LEFT JOIN channels c ON c.id = s.channel_id
		WHERE s.due_at < $1::timestamptz - interval '15 minutes' GROUP BY 1`, now)
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
		r.DebtHours[tier] = math.Round(h*10) / 10
	}
	rows.Close()
	if r.SupplyPerHour > 0 && r.DemandPerHour > r.SupplyPerHour {
		r.Stretch = math.Min(MaxStretch, math.Round(r.DemandPerHour/r.SupplyPerHour*100)/100)
	}
	if interval > 0 {
		perIP := 3600 / interval * Efficiency
		r.RecommendedIPs = int(math.Ceil(r.DemandPerHour / perIP))
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
	return fmt.Sprintf("supply %.0f/h, demand %.0f/h, stretch %.2f, recommended IPs %d, debt %v",
		r.SupplyPerHour, r.DemandPerHour, r.Stretch, r.RecommendedIPs, r.DebtHours)
}
