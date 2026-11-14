package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/discovery"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

// D-076: mention channels discovery admitted before the rule become core
// channels with the core jobs and their tier by size; the owner's own
// mention channels, and any set by hand, stay as they are.
func TestDiscoveredAsCore(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC)
	const disc, big, owners, byHand = "UCfakeD076discovered00001", "UCfakeD076discoveredBIG01", "UCfakeD076ownersmention01", "UCfakeD076sethand00000001"
	for _, c := range []struct{ id, by string }{{disc, "discovery"}, {big, "discovery"}, {owners, "admin"}, {byHand, "discovery"}} {
		if err := registry.List(ctx, pool, nil, c.id, registry.Input{Input: c.id, Kind: registry.Mention, AddedBy: c.by}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE channels SET subscribers_min = 2000000 WHERE id = $1`, big); err != nil {
		t.Fatal(err)
	}
	if err := registry.Set(ctx, pool, byHand, registry.Mention, policy.M); err != nil {
		t.Fatal(err)
	}

	n, err := discovery.DiscoveredAsCore(ctx, pool, now)
	if err != nil || n != 2 {
		t.Fatalf("moved %d (%v), want the 2 discovered, not set by hand", n, err)
	}
	for id, tier := range map[string]string{disc: "C", big: "A", owners: "M", byHand: "M"} {
		var kind, got string
		if err := pool.QueryRow(ctx, `SELECT kind, tier FROM channels WHERE id = $1`, id).Scan(&kind, &got); err != nil {
			t.Fatal(err)
		}
		if got != tier || (tier == "M") != (kind == registry.Mention) {
			t.Errorf("%s: kind %s, tier %s; want tier %s", id, kind, got, tier)
		}
	}
	for _, k := range []string{queue.KindBackfill, queue.KindTabs} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM schedule WHERE kind = $1 AND entity_id IN ($2, $3)`, k, disc, big).Scan(&n); err != nil || n != 2 {
			t.Errorf("%s jobs for the moved channels: %d (%v), want 2", k, n, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM schedule WHERE kind = $1 AND entity_id IN ($2, $3)`, k, owners, byHand).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s jobs for the channels left as mention: %d (%v), want 0", k, n, err)
		}
	}
	if n, err := discovery.DiscoveredAsCore(ctx, pool, now); err != nil || n != 0 {
		t.Errorf("a second run moved %d (%v), want 0", n, err)
	}
}
