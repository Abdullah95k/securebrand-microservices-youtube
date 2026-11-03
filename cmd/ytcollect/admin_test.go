package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

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
