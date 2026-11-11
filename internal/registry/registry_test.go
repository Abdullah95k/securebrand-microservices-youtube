package registry_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

const ch = "UCfakeREGISTRY0000000001"

func TestAddListSetUnlist(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	// Adding by handle queues a resolve job; nothing is listed yet.
	if err := registry.AddInput(ctx, pool, registry.Input{Input: " @fakechannel ", Tier: policy.A, Reason: "client brand"}); err != nil {
		t.Fatal(err)
	}
	in, err := registry.PendingInput(ctx, pool, "@fakechannel")
	if err != nil || in.Kind != registry.Core || in.Tier != policy.A || in.Reason != "client brand" {
		t.Fatalf("pending input %+v, err %v", in, err)
	}
	if err := registry.AddInput(ctx, pool, registry.Input{Input: "@x", Kind: registry.Mention, Tier: policy.A}); err == nil {
		t.Error("a mention channel in tier A must be refused")
	}
	// The resolve job lists it: its jobs are scheduled, backfill included.
	if err := registry.List(ctx, pool, nil, ch, in); err != nil {
		t.Fatal(err)
	}
	if err := registry.InputResolved(ctx, pool, "@fakechannel", ch, nil); err != nil {
		t.Fatal(err)
	}
	got := scheduled(t, pool)
	if want := "backfill channel playlists posts resolve rss tabs"; got != want {
		t.Errorf("jobs %q; want %q", got, want)
	}
	// Feed and tab checks find new uploads: P0, with the videos they find
	// (D-052). Refreshes are P2, backfill and playlists P3.
	prios := map[string]int{}
	rows, err := pool.Query(ctx, `SELECT kind, priority FROM schedule WHERE channel_id = $1`, ch)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		var p int
		if err := rows.Scan(&k, &p); err != nil {
			t.Fatal(err)
		}
		prios[k] = p
	}
	rows.Close()
	for k, want := range map[string]int{"rss": 0, "tabs": 0, "channel": 2, "posts": 2, "playlists": 3, "backfill": 3} {
		if prios[k] != want {
			t.Errorf("%s at P%d; want P%d", k, prios[k], want)
		}
	}
	c, err := registry.Get(ctx, pool, ch)
	if err != nil || c.Handle != "@fakechannel" || c.Status != "listed" || c.Tier != policy.A {
		t.Fatalf("channel %+v, err %v", c, err)
	}
	// Turning it into a mention channel drops tab checks and backfill.
	if err := registry.Set(ctx, pool, ch, registry.Mention, policy.M); err != nil {
		t.Fatal(err)
	}
	if got, want := scheduled(t, pool), "channel playlists posts resolve rss"; got != want {
		t.Errorf("jobs %q; want %q", got, want)
	}
	// Unlisting keeps the row and removes its jobs.
	if err := registry.Unlist(ctx, pool, ch, "no longer relevant"); err != nil {
		t.Fatal(err)
	}
	if got := scheduled(t, pool); got != "resolve" {
		t.Errorf("jobs after unlisting %q", got)
	}
	if list, _ := registry.Channels(ctx, pool, "dormant"); len(list) != 1 {
		t.Errorf("dormant channels %+v", list)
	}
	// Rearm brings back nothing for a dormant channel; listing again does.
	if n, err := registry.Rearm(ctx, pool); err != nil || n != 0 {
		t.Errorf("rearm added %d (err %v)", n, err)
	}
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schedule WHERE kind = 'rss'`); err != nil {
		t.Fatal(err)
	}
	if n, err := registry.Rearm(ctx, pool); err != nil || n != 1 {
		t.Errorf("rearm added %d (err %v); want the missing feed job", n, err)
	}
}

func TestSuppressedChannelCannotComeBack(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	if err := sup.Add(ctx, pool, privacy.Channel, ch, "erasure request", nil); err != nil {
		t.Fatal(err)
	}
	err := registry.List(ctx, pool, sup, ch, registry.Input{Input: ch, AddedBy: "discovery"})
	if !errors.Is(err, registry.ErrSuppressed) {
		t.Fatalf("err %v; want ErrSuppressed", err)
	}
	if list, _ := registry.Channels(ctx, pool, ""); len(list) != 0 {
		t.Error("a suppressed channel must not be stored")
	}
	if err := sup.Override(ctx, pool, privacy.Channel, ch, "the owner asked to be monitored again"); err != nil {
		t.Fatal(err)
	}
	if err := registry.List(ctx, pool, sup, ch, registry.Input{Input: ch}); err != nil {
		t.Errorf("after the override: %v", err)
	}
}

func scheduled(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT kind FROM schedule`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ks []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, " ")
}
