package sched_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/sched"
)

// A restart neither re-runs the daily and weekly tasks nor makes the hourly
// ones wait: due tasks run the shortest period first, and the longer ones'
// last runs are kept in settings.
func TestRestartKeepsLastRuns(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 0, 34, 0, 0, time.UTC)
	var ran []string
	tasks := func() []*sched.Task {
		task := func(name string, every time.Duration, err error) *sched.Task {
			return &sched.Task{Name: name, Every: every, Run: func() error { ran = append(ran, name); return err }}
		}
		// In the scheduler's order: the daily ones before the capacity plan.
		return []*sched.Task{
			task("metrics", time.Minute, nil),
			task("archive", 24*time.Hour, nil),
			task("privacy audit", 24*time.Hour, errors.New("fake failure")),
			task("capacity", time.Hour, nil),
			task("tiers", 7*24*time.Hour, nil),
		}
	}
	start := func() *sched.Runner {
		r := &sched.Runner{Q: pool, Now: func() time.Time { return now }, Tasks: tasks()}
		if err := r.Load(ctx); err != nil {
			t.Fatal(err)
		}
		return r
	}

	// First start: everything runs, the shortest period first.
	r := start()
	r.Tick(ctx)
	if want := []string{"metrics", "capacity", "archive", "privacy audit", "tiers"}; !slices.Equal(ran, want) {
		t.Fatalf("first start ran %q, want %q", ran, want)
	}
	var saved time.Time
	if ok, err := db.GetSetting(ctx, pool, sched.KeyPrefix+"privacy audit", &saved); err != nil || !ok || !saved.Equal(now) {
		t.Errorf("a failed run counts too (tried again a period later): %v %v %v", saved, ok, err)
	}
	if ok, _ := db.GetSetting(ctx, pool, sched.KeyPrefix+"metrics", &saved); ok {
		t.Error("the every-minute metrics are not saved")
	}

	// A restart 20 minutes later (a deploy): only the metrics run.
	now = now.Add(20 * time.Minute)
	ran = nil
	r = start()
	r.Tick(ctx)
	if want := []string{"metrics"}; !slices.Equal(ran, want) {
		t.Fatalf("after a restart ran %q, want %q", ran, want)
	}

	// An hour after the first runs the capacity plan is due again, on time.
	now = now.Add(40 * time.Minute)
	ran = nil
	r.Tick(ctx)
	if want := []string{"metrics", "capacity"}; !slices.Equal(ran, want) {
		t.Fatalf("an hour on ran %q, want %q", ran, want)
	}

	// A day on, after another restart: the daily tasks are due, the weekly
	// one isn't; the hourly plan still goes before the daily ones.
	now = now.Add(24 * time.Hour)
	ran = nil
	r = start()
	r.Tick(ctx)
	if want := []string{"metrics", "capacity", "archive", "privacy audit"}; !slices.Equal(ran, want) {
		t.Fatalf("a day on ran %q, want %q", ran, want)
	}
}

// A run cut short by shutdown isn't counted, so the next scheduler does it
// again; a saved time later than now is ignored.
func TestShutdownAndFutureTimes(t *testing.T) {
	pool := dbtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, 11, 14, 1, 56, 0, 0, time.UTC)
	if err := db.SetSetting(ctx, pool, sched.KeyPrefix+"capacity", now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var ran []string
	archive := &sched.Task{Name: "archive", Every: 24 * time.Hour, Run: func() error { ran = append(ran, "archive"); cancel(); return context.Canceled }}
	capacity := &sched.Task{Name: "capacity", Every: time.Hour, Run: func() error { ran = append(ran, "capacity"); return nil }}
	r := &sched.Runner{Q: pool, Now: func() time.Time { return now }, Tasks: []*sched.Task{archive, capacity}}
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if !capacity.Last().IsZero() {
		t.Errorf("a saved time in the future is ignored, got %v", capacity.Last())
	}
	r.Tick(ctx)
	if want := []string{"capacity", "archive"}; !slices.Equal(ran, want) {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	if !archive.Last().IsZero() {
		t.Error("an interrupted run is not counted")
	}
	var saved time.Time
	if ok, err := db.GetSetting(context.Background(), pool, sched.KeyPrefix+"archive", &saved); err != nil || ok {
		t.Errorf("an interrupted run is not saved: %v %v", ok, err)
	}
}
