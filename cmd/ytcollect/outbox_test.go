package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
)

func TestOutboxCommand(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, sql := range []string{
		`INSERT INTO videos (id, channel_id, kind, title, live_status, published_at, published_precision_s)
			VALUES ('fakeOBv1', 'UCfakeOUTBOXCMD000000001', 'video', 'A video', 'none', '2026-11-07 08:00:00+00', 0)`,
		// A channel with no name: the contract refuses its account.
		`INSERT INTO channels (id, kind, tier, status) VALUES ('UCfakeOUTBOXCMD000000001', 'core', 'B', 'listed')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	ev := outbox.New(ctx, at)
	if err := ev.Write(ctx, pool, outbox.Videos, "fakeOBv1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ev.Content(ctx, pool, outbox.Channels, "UCfakeOUTBOXCMD000000001"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := outboxCmd(ctx, pool, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "3 events waiting") || !strings.Contains(out.String(), "video_metrics") {
		t.Errorf("status:\n%s", out.String())
	}
	out.Reset()
	err := outboxCmd(ctx, pool, &out, []string{"check"})
	if err == nil || !strings.Contains(err.Error(), "1 of 3 events") {
		t.Errorf("check: err %v", err)
	}
	for _, want := range []string{"content.post.v1", "content.metrics.v1", "channel UCfakeOUTBOXCMD000000001 (refused by the contract)",
		"neither display_name nor handle"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("check output lacks %q:\n%s", want, out.String())
		}
	}
	if err := outboxCmd(ctx, pool, &out, []string{"nonsense"}); err == nil {
		t.Error("an unknown subcommand is an error")
	}
}
