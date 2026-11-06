package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/usage"
)

func TestParseIraqTime(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2026-11-10":           time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC),
		"2026-11-10 14:00":     time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC),
		" 2026-11-10T14:00 ":   time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC),
		"2026-11-10T14:00:00Z": time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC),
	} {
		got, err := parseIraqTime(in)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseIraqTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseIraqTime("yesterday"); err == nil {
		t.Error("a non-time must be refused")
	}
}

func TestReportCommand(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, usage.Iraq)
	for _, args := range [][]string{
		{"-hours", "0"},
		{"-from", "2026-11-12", "-to", "2026-11-10"},
		{"-channels", "1000,many"},
		{"-gap", "-1s"},
		{"extra"},
	} {
		var out strings.Builder
		if err := reportCmd(ctx, pool, &out, args, now); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	// One monitored channel with a day of comment reads.
	if _, err := pool.Exec(ctx, `INSERT INTO channels (id, tier, status) VALUES ('UCfakeREPORTAAAAAAAAAAAA', 'B', 'listed')`); err != nil {
		t.Fatal(err)
	}
	start := now.Add(-24 * time.Hour)
	for i := 0; i < 24; i++ {
		j := queue.Job{Kind: queue.KindComments, EntityID: "fakevid0001", ChannelID: "UCfakeREPORTAAAAAAAAAAAA"}
		if err := usage.Record(ctx, pool, start.Add(time.Duration(i)*time.Hour), j, usage.Totals{Requests: 10, Bytes: 50000}, false, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	if err := reportCmd(ctx, pool, &out, []string{"-hours", "24", "-channels", "1000,50000"}, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# yts usage report", "| B | 1 | 240 |", "| 1,000 | 240,000 |", "| 50,000 | 12,000,000 |"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	// JSON, to a file.
	file := filepath.Join(t.TempDir(), "report.json")
	out.Reset()
	if err := reportCmd(ctx, pool, &out, []string{"-from", "2026-11-11", "-to", "2026-11-12", "-json", "-o", file}, now); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var r usage.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Channels != 1 || r.Total.Requests != 240 || len(r.Projections) != 3 {
		t.Errorf("JSON report = %d channels, %d requests, %d projections", r.Channels, r.Total.Requests, len(r.Projections))
	}
	if !strings.Contains(out.String(), "Report written to") {
		t.Errorf("output = %q", out.String())
	}
}
