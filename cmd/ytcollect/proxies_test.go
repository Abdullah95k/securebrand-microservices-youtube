package main

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
)

func TestProxiesCommand(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO proxy_health (proxy_key, label, subnet, state, quarantined_since, updated_at) VALUES
		('px-0a0a0a', 'px-0a0a0a', 'sn-1', 'quarantined', now() - interval '1 hour', now()),
		('px-0b0b0b', 'px-0b0b0b', 'sn-2', 'quarantined', now() - interval '2 hours', now()),
		('px-0c0c0c', 'px-0c0c0c', 'sn-3', 'ok', NULL, now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE proxy_health SET blocks_24h = 5, player_blocks = 4 WHERE proxy_key = 'px-0c0c0c'`); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var b strings.Builder
		err := proxiesCmd(ctx, pool, &b, args, time.Now())
		return b.String(), err
	}
	out, err := run()
	if err != nil || !strings.Contains(out, "px-0a0a0a") || !strings.Contains(out, "quarantined") || strings.Count(out, "\n") != 4 {
		t.Fatalf("list (%v):\n%s", err, out)
	}
	if !strings.Contains(out, "PAGE BLOCKS") || !regexp.MustCompile(`px-0c0c0c .* 1 +4 +`).MatchString(out) {
		t.Errorf("want 1 page block and 4 player blocks for px-0c0c0c:\n%s", out)
	}
	if out, err := run("release", "px-0a0a0a"); err != nil || !strings.Contains(out, "1 IP(s) released") {
		t.Fatalf("release one (%v): %s", err, out)
	}
	if _, err := run("release", "px-0a0a0a"); err == nil {
		t.Error("releasing an IP twice must say it isn't quarantined")
	}
	if _, err := run("release", "px-0c0c0c"); err == nil {
		t.Error("an IP that isn't quarantined can't be released")
	}
	if out, _ := run(); !strings.Contains(out, "quarantined (released)") {
		t.Errorf("the list shows the release until the worker applies it:\n%s", out)
	}
	if out, err := run("release", "-all"); err != nil || !strings.Contains(out, "1 IP(s) released") {
		t.Errorf("release -all (%v): %s", err, out)
	}
	if out, err := run("release", "-all"); err != nil || !strings.Contains(out, "no IP is quarantined") {
		t.Errorf("release -all again (%v): %s", err, out)
	}
	if _, err := run("frobnicate"); err == nil {
		t.Error("unknown arguments must fail")
	}
}
