// Package sched runs the scheduler's periodic tasks, one after another.
//
// A task that runs an hour or more apart keeps its last run in settings
// (scheduler/last/<task>), so a restart (a deploy) neither re-runs the
// daily and weekly tasks nor makes the hourly ones, such as the capacity
// plan, wait behind them. Due tasks run the shortest period first, so an
// hourly task never waits behind a daily one that falls due with it.
package sched

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
)

// KeyPrefix starts the settings keys that keep the tasks' last runs.
const KeyPrefix = "scheduler/last/"

// Keep is the shortest period whose last run is kept across restarts;
// shorter tasks (the metrics, every minute) run at start as before.
const Keep = time.Hour

// Task is one periodic job.
type Task struct {
	Name  string
	Every time.Duration
	Run   func() error

	last time.Time
}

// Last is when the task last started (zero: not yet).
func (t *Task) Last() time.Time { return t.last }

// Runner runs the tasks.
type Runner struct {
	Q     db.Querier
	Log   *slog.Logger
	Now   func() time.Time
	Tasks []*Task
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Load reads the last runs an earlier scheduler saved. A time later than
// now (a clock gone wrong) is ignored, so the task can't be held back.
func (r *Runner) Load(ctx context.Context) error {
	rows, err := r.Q.Query(ctx, `SELECT key, value FROM settings WHERE key LIKE 'scheduler/last/%'`)
	if err != nil {
		return err
	}
	saved := map[string]time.Time{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return err
		}
		var at time.Time
		if json.Unmarshal(raw, &at) == nil {
			saved[strings.TrimPrefix(key, KeyPrefix)] = at
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := r.now()
	for _, t := range r.Tasks {
		if at, ok := saved[t.Name]; ok && t.Every >= Keep && !at.After(now) {
			t.last = at
		}
	}
	return nil
}

// Tick runs every due task, the shortest period first. A task's run counts
// (and, for the longer ones, is saved) when it ends, whether it worked or
// not, as before: a failure is tried again a period later. A run cut short
// by shutdown is not counted, so the next scheduler does it again.
func (r *Runner) Tick(ctx context.Context) {
	now := r.now()
	var due []*Task
	for _, t := range r.Tasks {
		if now.Sub(t.last) >= t.Every {
			due = append(due, t)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].Every < due[j].Every })
	for _, t := range due {
		if ctx.Err() != nil {
			return
		}
		started := r.now()
		err := t.Run()
		if ctx.Err() != nil {
			return
		}
		t.last = started
		if err != nil {
			r.log().Warn("scheduler task failed", "task", t.Name, "err", err)
		}
		if t.Every >= Keep {
			if err := db.SetSetting(ctx, r.Q, KeyPrefix+t.Name, started); err != nil && ctx.Err() == nil {
				r.log().Warn("saving a scheduler task's last run failed", "task", t.Name, "err", err)
			}
		}
	}
}

// Run loads the saved last runs, then ticks every minute until ctx ends.
// If the saved runs can't be read, every task runs at start, as before.
func (r *Runner) Run(ctx context.Context) {
	if err := r.Load(ctx); err != nil && ctx.Err() == nil {
		r.log().Warn("reading the scheduler tasks' last runs failed; running every task now", "err", err)
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
