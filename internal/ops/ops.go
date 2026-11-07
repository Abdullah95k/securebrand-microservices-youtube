// Package ops serves the operations endpoints of every long-running yts
// process (standard B1), on its ops port:
//
//   - /healthz: the process is alive. Always 200 while it runs, also while
//     it is shutting down, so a slow, orderly shutdown is never killed as
//     "dead". It calls no store (REQ-024).
//   - /readyz: its dependencies answer (the database, and for workers a
//     usable proxy), and it isn't shutting down. 200, or 503 with the names
//     of the checks that failed. Error texts are never shown: they can
//     name hosts. The checks run at most once a minute; in between, /readyz
//     answers from the last result, a failure included (REQ-024: a probe
//     every 10 seconds would otherwise keep a PostgreSQL connection busy).
//   - /metrics: Prometheus, when a metrics handler is given.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultCheckTimeout bounds each readiness check. Each check has its own
// limit, not the request's, so a probe that gives up first can't cancel a
// check into a false failure (the chart's probes wait 5 seconds).
const DefaultCheckTimeout = 3 * time.Second

// DefaultEvery is how often the readiness checks run at most (REQ-024).
const DefaultEvery = time.Minute

// Health holds the readiness checks and the draining flag.
type Health struct {
	// Timeout bounds each check (DefaultCheckTimeout when zero).
	Timeout time.Duration
	// Every is how often the checks run at most (DefaultEvery when zero;
	// negative: on every request).
	Every time.Duration
	// Now is the clock (time.Now when nil).
	Now func() time.Time

	mu       sync.Mutex
	checks   []check
	draining atomic.Bool

	run    sync.Mutex // one run of the checks at a time
	ran    bool
	at     time.Time
	failed []string
}

type check struct {
	name string
	fn   func(context.Context) error
}

// Add registers a readiness check. fn returns nil when the dependency
// answers.
func (h *Health) Add(name string, fn func(context.Context) error) {
	h.mu.Lock()
	h.checks = append(h.checks, check{name, fn})
	h.mu.Unlock()
}

// Drain marks the process as shutting down: /readyz fails from now on.
func (h *Health) Drain() { h.draining.Store(true) }

// Draining reports whether Drain was called.
func (h *Health) Draining() bool { return h.draining.Load() }

func (h *Health) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Failed returns the names of the checks that failed: the last result when
// the checks ran less than Every ago, else a new one. The checks run side
// by side, each on its own context bounded by Timeout (ctx's values are
// kept, its end is not).
func (h *Health) Failed(ctx context.Context) []string {
	h.run.Lock()
	defer h.run.Unlock()
	every := h.Every
	if every == 0 {
		every = DefaultEvery
	}
	if h.ran && every > 0 && h.now().Sub(h.at) < every {
		return append([]string(nil), h.failed...)
	}
	h.mu.Lock()
	checks := append([]check(nil), h.checks...)
	h.mu.Unlock()
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	errs := make([]error, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
			defer cancel()
			errs[i] = c.fn(cctx)
		}()
	}
	wg.Wait()
	var failed []string
	for i, c := range checks {
		if errs[i] != nil {
			failed = append(failed, c.name)
		}
	}
	h.ran, h.at, h.failed = true, h.now(), failed
	return append([]string(nil), failed...)
}

type status struct {
	Status string   `json:"status"`
	Failed []string `json:"failed,omitempty"`
}

func reply(w http.ResponseWriter, code int, s status) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(s)
}

// Register adds /healthz and /readyz to mux.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, status{Status: "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if h.Draining() {
			reply(w, http.StatusServiceUnavailable, status{Status: "draining"})
			return
		}
		if failed := h.Failed(r.Context()); len(failed) > 0 {
			reply(w, http.StatusServiceUnavailable, status{Status: "not ready", Failed: failed})
			return
		}
		reply(w, http.StatusOK, status{Status: "ready"})
	})
}

// Mux returns a mux with /healthz, /readyz and, when metrics isn't nil,
// /metrics.
func Mux(h *Health, metrics http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	h.Register(mux)
	if metrics != nil {
		mux.Handle("/metrics", metrics)
	}
	return mux
}

// Serve runs handler on addr until ctx ends ("" turns it off). Give it a
// context that lasts as long as the process, not the shutdown signal, so
// /healthz keeps answering while the process drains.
func Serve(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) {
	if addr == "" {
		return
	}
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) && log != nil {
			log.Warn("ops endpoint stopped", "addr", addr, "err", err)
		}
	}()
}
