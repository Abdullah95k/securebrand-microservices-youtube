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
