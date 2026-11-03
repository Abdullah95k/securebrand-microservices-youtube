package capacity_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/capacity"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

func TestPlanStretchesWhenDemandExceedsSupply(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Now()
	// One worker: 2 proxies at one request per 4 s = 1,440 requests/h after efficiency.
	if err := capacity.Announce(ctx, pool, "w1", capacity.Worker{Proxies: 2, IntervalS: 4, At: now}); err != nil {
		t.Fatal(err)
	}
	_ = capacity.Announce(ctx, pool, "stale", capacity.Worker{Proxies: 50, IntervalS: 4, At: now.Add(-time.Hour)})
	_ = registry.List(ctx, pool, nil, "UCfakeCAPACITY0000000001", registry.Input{Input: "UCfakeCAPACITY0000000001", Tier: policy.C})
	// 18,000 comment scans due within 24 h at 4 requests each: 3,000/h.
	if _, err := pool.Exec(ctx, `INSERT INTO schedule (kind, entity_id, priority, due_at)
		SELECT 'comments', 'fakeV' || i, 1, $1::timestamptz + make_interval(secs => i * 4) FROM generate_series(0, 17999) i`, now); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `UPDATE schedule SET channel_id = 'UCfakeCAPACITY0000000001', due_at = now() - interval '2 hours' WHERE kind = 'rss'`)
	r, err := capacity.Plan(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Workers != 1 || r.SupplyPerHour != 1440 {
		t.Errorf("supply %v from %d workers; the stale worker must not count", r.SupplyPerHour, r.Workers)
	}
	if r.DemandPerHour < 3000 || r.DemandPerHour > 3010 {
		t.Errorf("demand %v; want about 3,000/h", r.DemandPerHour)
	}
	if r.Stretch < 2.08 || r.Stretch > 2.1 || r.RecommendedIPs != 5 {
		t.Errorf("stretch %v, recommended IPs %d; want about 2.08 and 5", r.Stretch, r.RecommendedIPs)
	}
	if r.DebtHours["C"] == 0 {
		t.Error("the channel's overdue jobs are tier C debt")
	}
	st := &capacity.Stretcher{Q: pool}
	if v := st.Value(); v != r.Stretch {
		t.Errorf("workers read stretch %v", v)
	}
	// Plenty of supply: no stretch.
	_, _ = pool.Exec(ctx, `DELETE FROM schedule WHERE kind = 'comments'`)
	if r, _ := capacity.Plan(ctx, pool, now.Add(time.Minute)); r.Stretch != 1 {
		t.Errorf("stretch %v with no backlog", r.Stretch)
	}
	if policy.Stretch(time.Hour, policy.A, 4) != time.Hour {
		t.Error("tier A is never stretched")
	}
}
