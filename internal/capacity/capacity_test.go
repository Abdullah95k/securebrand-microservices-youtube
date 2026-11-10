package capacity_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/capacity"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f; want %.3f", what, got, want)
	}
}

// fixture is a tier C channel with 6,000 comment scans due today
// (stretchable) and 1,200 searches (fixed), on one worker with 2 IPs at a
// 4 s gap, and the planner's first run on it.
type fixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	at    time.Time
	first capacity.Report
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, pool: dbtest.New(t), at: time.Now()}
	ctx := context.Background()
	// 2 IPs, a request every 4 s + 0.7 s answer: 766 an hour each, 536 at
	// 70% busy: 1,072 an hour.
	if err := capacity.Announce(ctx, f.pool, "w1", capacity.Worker{Proxies: 2, IntervalS: 4, At: f.at}); err != nil {
		t.Fatal(err)
	}
	_ = capacity.Announce(ctx, f.pool, "stale", capacity.Worker{Proxies: 50, IntervalS: 4, At: f.at.Add(-time.Hour)})
	if err := registry.List(ctx, f.pool, nil, "UCfakeCAPACITY0000000001", registry.Input{Input: "UCfakeCAPACITY0000000001", Tier: policy.C}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		// 6,000 comment scans at 4 requests: 1,000 an hour, stretchable.
		`INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at)
			SELECT 'comments', 'fakeV' || i, 'UCfakeCAPACITY0000000001', 1, $1::timestamptz + make_interval(secs => i * 14) FROM generate_series(0, 5999) i`,
		// 1,200 searches at 4 requests: 200 an hour, fixed.
		`INSERT INTO schedule (kind, entity_id, priority, due_at)
			SELECT 'search', 'fake term ' || i, 2, $1::timestamptz + make_interval(secs => i * 70) FROM generate_series(0, 1199) i`,
		// 2,400 images at P4: spare-time work, never in the demand.
		`INSERT INTO schedule (kind, entity_id, priority, due_at)
			SELECT 'media', 'thumb:fakeIMG' || i, 4, $1::timestamptz + make_interval(secs => i * 35) FROM generate_series(0, 2399) i`,
		`UPDATE schedule SET due_at = $1::timestamptz - interval '2 hours' WHERE kind = 'rss'`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.at); err != nil {
			t.Fatal(err)
		}
	}
	f.first = f.plan(capacity.Options{})
	return f
}

// plan runs the planner a minute after the last run.
func (f *fixture) plan(o capacity.Options) capacity.Report {
	f.t.Helper()
	f.at = f.at.Add(time.Minute)
	r, err := capacity.PlanWith(context.Background(), f.pool, f.at, o)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) setStretch(v float64) {
	f.t.Helper()
	if err := db.SetSetting(context.Background(), f.pool, capacity.StretchKey, v); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) exec(sql string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql); err != nil {
		f.t.Fatal(err)
	}
}

func TestPlanFitsTheIPs(t *testing.T) {
	f := setup(t)
	r := f.first
	supply := 2 * 3600 / 4.7 * 0.7
	if r.Workers != 1 {
		t.Errorf("%d workers; the stale one must not count", r.Workers)
	}
	near(t, "supply", r.SupplyPerHour, supply, 0.01)
	// Fixed: the searches (200), and the channel's feed (2) and backfill (40)
	// once each today. Stretchable: the comment scans (1,000), and the
	// channel snapshot (2), posts (2) and playlists (4) once each.
	near(t, "fixed", r.FixedPerHour, 200+42.0/24, 0.01)
	near(t, "stretchable", r.StretchablePerHour, 1000+8.0/24, 0.01)
	near(t, "demand", r.DemandPerHour, r.FixedPerHour+r.StretchablePerHour, 0.001)
	near(t, "spare-time work", r.SpareTimePerHour, 2400*2.0/24, 0.01) // images, 2 requests each by the estimate
	// 1,000.3 / (1,072.3 - 201.75) = 1.149.
	near(t, "stretch", r.Stretch, 1.15, 0.001)
	if r.RecommendedIPs != 3 {
		t.Errorf("recommended IPs %d; want 3 (1,202 an hour at 536 each)", r.RecommendedIPs)
	}
	if r.DebtHours["C"] == 0 {
		t.Error("the channel's overdue feed check is tier C debt")
	}
	if v := (&capacity.Stretcher{Q: f.pool}).Value(); v != r.Stretch {
		t.Errorf("workers read stretch %v", v)
	}
	if policy.Stretch(time.Hour, policy.A, 4) != time.Hour {
		t.Error("tier A is never stretched")
	}
}

// The stretch in force thins out the stretched jobs due today, so the
// planner counts them at that much: the same schedule under a stretch of 2
// is 2,000 an hour of stretchable work.
func TestPlanAllowsForTheStretchInForce(t *testing.T) {
	f := setup(t)
	f.setStretch(2)
	r := f.plan(capacity.Options{})
	near(t, "stretchable", r.StretchablePerHour, 2*(1000+8.0/24), 0.01)
	// 2,000.7 / 870.6 = 2.298.
	near(t, "stretch", r.Stretch, 2.3, 0.001)
}

// A kind's measured cost replaces the estimate once 20 jobs finished in
// the last day; failed jobs don't count. The stretch moves at most 1.5
// times per run.
func TestPlanUsesMeasuredCosts(t *testing.T) {
	f := setup(t)
	f.setStretch(1)
	f.exec(`INSERT INTO usage_hourly (hour, kind, jobs, failures, requests, bytes, blocks, run_ms)
		VALUES (date_trunc('hour', now(), 'UTC'), 'comments', 110, 10, 800, 0, 0, 0),
		       (date_trunc('hour', now(), 'UTC'), 'search', 15, 0, 1500, 0, 0, 0)`)
	r := f.plan(capacity.Options{})
	if r.Measured["comments"] != 8 {
		t.Errorf("measured %v; want comments at 8 requests a job", r.Measured)
	}
	if _, ok := r.Measured["search"]; ok {
		t.Error("15 searches are too few to replace the estimate")
	}
	near(t, "stretchable", r.StretchablePerHour, 2000+8.0/24, 0.01)
	near(t, "stretch", r.Stretch, 1.5, 0.001) // wants 2.3; one step from 1
	r = f.plan(capacity.Options{})
	near(t, "stretch", r.Stretch, 2.25, 0.001) // the next step: 1.5 x 1.5
}

// The owner's floor applies at once and whatever the demand; without a
// live worker the stretch stays as it is.
func TestPlanFloorAndNoWorker(t *testing.T) {
	f := setup(t)
	f.exec(`DELETE FROM schedule WHERE kind IN ('comments', 'search')`)
	f.setStretch(1)
	if r := f.plan(capacity.Options{MinStretch: 3}); r.Stretch != 3 || r.MinStretch != 3 {
		t.Errorf("stretch %v (floor %v); want the floor, 3", r.Stretch, r.MinStretch)
	}
	if r := f.plan(capacity.Options{MinStretch: 20}); r.Stretch != capacity.MaxStretch {
		t.Errorf("stretch %v; the floor can't pass %v", r.Stretch, capacity.MaxStretch)
	}
	if r := f.plan(capacity.Options{}); r.Stretch != 5.33 {
		t.Errorf("stretch %v; with little demand it falls one step at a time (8 / 1.5)", r.Stretch)
	}
	f.exec(`DELETE FROM settings WHERE key = 'capacity/w1'`)
	f.setStretch(2.5)
	if r := f.plan(capacity.Options{}); r.Stretch != 2.5 || r.SupplyPerHour != 0 {
		t.Errorf("stretch %v with no live worker; want it kept at 2.5", r.Stretch)
	}
}

// A worker that stopped is no longer supply, even while its announcement is
// fresh: the pod that replaced it announces itself too (10 November: the
// first plan after a restart counted 10 IPs twice).
func TestPlanSkipsAStoppedWorker(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	old := capacity.Worker{Proxies: 2, IntervalS: 4, At: f.at}
	if err := capacity.Announce(ctx, f.pool, "w-old", old); err != nil {
		t.Fatal(err)
	}
	if r := f.plan(capacity.Options{}); r.Workers != 2 {
		t.Fatalf("%d workers before the stop", r.Workers)
	}
	if err := capacity.Withdraw(ctx, f.pool, "w-old", old, f.at); err != nil {
		t.Fatal(err)
	}
	r := f.plan(capacity.Options{})
	if r.Workers != 1 {
		t.Errorf("%d workers; the stopped one must not count", r.Workers)
	}
	near(t, "supply", r.SupplyPerHour, 2*3600/4.7*0.7, 0.01)
}

// Overdue P4 work (images, enrichment) runs in spare time: its backlog is
// reported apart and never counts as a tier's freshness debt.
func TestPlanKeepsSpareTimeBacklogOutOfDebt(t *testing.T) {
	f := setup(t)
	f.exec(`UPDATE schedule SET due_at = now() - interval '5 hours' WHERE kind = 'media' AND entity_id IN ('thumb:fakeIMG0', 'thumb:fakeIMG1')`)
	f.exec(`INSERT INTO schedule (kind, entity_id, priority, due_at) VALUES ('search', 'fake overdue term', 2, now() - interval '3 hours')`)
	r := f.plan(capacity.Options{})
	near(t, "spare-time debt", r.SpareDebtHours, 10, 0.2)
	if _, ok := r.DebtHours["spare"]; ok {
		t.Error("spare-time work must not be a tier")
	}
	near(t, "debt of no listed channel", r.DebtHours["-"], 3, 0.2)
}
