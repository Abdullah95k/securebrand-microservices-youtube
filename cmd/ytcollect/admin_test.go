package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestChannelsAndHealthCommands(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	list := filepath.Join(t.TempDir(), "channels.txt")
	_ = os.WriteFile(list, []byte("# my list\n@fakeone\n\nhttps://www.youtube.com/@faketwo  # note\n"), 0o600)
	var out strings.Builder
	if err := channelsCmd(ctx, pool, &out, []string{"import", "-tier", "B", list}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 channel(s) queued") {
		t.Errorf("import output %q", out.String())
	}
	if in, err := registry.PendingInput(ctx, pool, "https://www.youtube.com/@faketwo"); err != nil || in.Tier != policy.B {
		t.Errorf("input %+v, err %v", in, err)
	}
	if err := channelsCmd(ctx, pool, &out, []string{"add", "-kind", "mention", "-tier", "A", "@x"}); err == nil {
		t.Error("a mention channel in tier A must be refused")
	}
	if err := registry.List(ctx, pool, nil, "UCfakeADMIN0000000000001", registry.Input{Input: "@fakeone", Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	if err := channelsCmd(ctx, pool, &out, []string{"set", "-tier", "a", "UCfakeADMIN0000000000001"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := channelsCmd(ctx, pool, &out, []string{"list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "UCfakeADMIN0000000000001  @fakeone  core  A") {
		t.Errorf("list output:\n%s", out.String())
	}
	out.Reset()
	if err := switchCmd(ctx, pool, &out, []string{"backfill", "off"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := healthCmd(ctx, pool, &out); err != nil {
		t.Fatal(err)
	}
	h := out.String()
	for _, want := range []string{"P0        resolve", "backfill", "OFF", "Listed channels: 1."} {
		if !strings.Contains(h, want) {
			t.Errorf("health misses %q:\n%s", want, h)
		}
	}
	if err := channelsCmd(ctx, pool, &out, []string{"unlist", "-reason", "test", "UCfakeADMIN0000000000001"}); err != nil {
		t.Fatal(err)
	}
}

func TestKeywordsCommand(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	var out strings.Builder
	if err := keywordsCmd(ctx, pool, &out, []string{"import-seed"}); err != nil {
		t.Fatal(err)
	}
	if err := keywordsCmd(ctx, pool, &out, []string{"add", "-kind", "brand", "-variants", "Zqbrand|زيكيو", "Zqbrand"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := keywordsCmd(ctx, pool, &out, []string{"list"}); err != nil {
		t.Fatal(err)
	}
	line := func() []string {
		for _, l := range strings.Split(out.String(), "\n") {
			if f := strings.Fields(l); len(f) > 1 && f[1] == "Zqbrand" {
				return f
			}
		}
		return nil
	}
	f := line()
	if len(f) < 9 || f[2] != "brand" || f[3] != "admin" || f[4] != "true" || f[6] != "false" || !strings.Contains(out.String(), "Zqbrand | زيكيو") {
		t.Fatalf("list output:\n%s", out.String())
	}
	id := f[0]
	if err := keywordsCmd(ctx, pool, &out, []string{"set", "-active=false", id}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_ = keywordsCmd(ctx, pool, &out, []string{"list"})
	if f := line(); len(f) < 5 || f[4] != "false" {
		t.Errorf("after set:\n%s", out.String())
	}
	if err := keywordsCmd(ctx, pool, &out, []string{"remove", id}); err != nil {
		t.Fatal(err)
	}
}

func TestEraseAndExportCommands(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	key := filepath.Join(t.TempDir(), "privacy_key")
	_ = os.WriteFile(key, []byte("test-secret-0123456789abcdef-0123456789"), 0o600)
	t.Setenv(EnvPrivacyKeyFile, key)
	if err := registry.List(ctx, pool, nil, "UCfakeADMIN0000000000002", registry.Input{Input: "UCfakeADMIN0000000000002"}); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, relevant) VALUES ('fakeXv1', 'UCfakeADMIN0000000000002', 'video', true)`)
	_, _ = pool.Exec(ctx, `INSERT INTO comments (id, parent_kind, parent_id, channel_id, author_id, author_handle, text, text_hash, first_seen, last_seen)
		VALUES ('fakeXc1', 'video', 'fakeXv1', 'UCfakeADMIN0000000000002', 'UCfakeADMINAUTHOR0000001', '@someone', 'hello', '\x00', now(), now())`)
	var out strings.Builder
	if err := eraseCmd(ctx, pool, quietLog, &out, []string{"-kind", "planet", "x"}); err == nil {
		t.Error("an unknown kind must be refused")
	}
	if err := eraseCmd(ctx, pool, quietLog, &out, []string{"-kind", "comment", "-by", "ticket 9", "fakeXc1"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := erasuresCmd(ctx, pool, &out); err != nil || !strings.Contains(out.String(), "comment  pending  ticket 9") {
		t.Errorf("erasures: %v\n%s", err, out.String())
	}
	file := filepath.Join(t.TempDir(), "export.jsonl")
	if err := exportCmd(ctx, pool, quietLog, []string{"-pseudonymize", "-relevant", "-out", file}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), `"author_key":"`) || strings.Contains(string(data), "someone") || strings.Contains(string(data), "UCfakeADMINAUTHOR") {
		t.Errorf("a pseudonymized export must carry keys, not identities:\n%s", data)
	}
	if err := privacyAuditCmd(ctx, pool, quietLog, &out); err != nil {
		t.Fatal(err)
	}
}

// The scheduler refuses an outbox keep under an hour and a stretch out of
// range before it starts anything.
func TestSchedulerRefusesBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-outbox-keep=30m"},
		{"-min-stretch=9"},
	} {
		if err := schedulerCmd(context.Background(), nil, quietLog, append([]string{"-metrics-addr="}, args...)); err == nil {
			t.Errorf("scheduler %v: want a refusal", args)
		}
	}
}
