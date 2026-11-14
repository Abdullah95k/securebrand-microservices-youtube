package discovery_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/discovery"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

// D-076 option 2: a non-Iraqi channel is admitted as core once at least 10%
// of its recent items match; below that it is a mention channel, as before.
// Iraqi channels and channels whose own page matches are core, as before.
func TestClassifyTenPercent(t *testing.T) {
	now := time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC)
	match := func(s string) bool { return strings.Contains(s, "Basra") }
	recent := func(matching, total int) []string {
		var out []string
		for i := 0; i < total; i++ {
			if i < matching {
				out = append(out, fmt.Sprintf("Basra %d", i))
			} else {
				out = append(out, fmt.Sprintf("Kitchen %d", i))
			}
		}
		return out
	}
	for _, c := range []struct {
		name     string
		country  string
		own      bool
		matching int
		total    int
		hits     []string
		kind     string
	}{
		{"1 of 30 recent: under 10%", "", false, 1, 30, nil, registry.Mention},
		{"3 of 30 recent: exactly 10%", "", false, 3, 30, nil, registry.Core},
		{"2 of 15 recent: 13%", "", false, 2, 15, nil, registry.Core},
		{"no recent match, a search hit only", "", false, 0, 30, []string{"Basra news"}, registry.Mention},
		{"Iraqi channel, one match", "Iraq", false, 1, 30, nil, registry.Core},
		{"own page matches", "", true, 0, 30, nil, registry.Core},
	} {
		d := discovery.Classify(c.country, c.own, recent(c.matching, c.total), c.hits, match, 50_000, now, now)
		if !d.List || d.Kind != c.kind {
			t.Errorf("%s: list %v, kind %q; want %q", c.name, d.List, d.Kind, c.kind)
		}
		if d.Kind == registry.Mention && d.Tier != policy.M || d.Kind == registry.Core && d.Tier != policy.C {
			t.Errorf("%s: tier %q", c.name, d.Tier)
		}
	}
}

// A mention channel discovery admitted moves to core, with its tier, tab
// checks and backfill, once 10% of its latest feed uploads match; the
// owner's own mention channels, and any set by hand, never move.
func TestMentionToCore(t *testing.T) {
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
	move := func(id string, matching, total int) bool {
		t.Helper()
		moved, err := discovery.MentionToCore(ctx, pool, id, matching, total, now)
		if err != nil {
			t.Fatal(err)
		}
		return moved
	}
	if move(disc, 1, 15) || move(disc, 0, 0) {
		t.Error("1 of 15 (7%), or no uploads, is under the bar: the channel stays a mention channel")
	}
	if !move(disc, 2, 15) || !move(big, 3, 15) {
		t.Error("2 or 3 of 15 (13%, 20%) reach the bar: the discovered channels move to core")
	}
	if move(owners, 15, 15) || move(byHand, 15, 15) {
		t.Error("the owner's mention channel and one set by hand never move")
	}
	if move(disc, 15, 15) {
		t.Error("a channel already core is not moved again")
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
}
