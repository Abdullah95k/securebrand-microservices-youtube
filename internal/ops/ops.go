// Package ops serves the operations endpoints of every long-running yts
// process (standard B1), on its ops port:
//
//   - /healthz: the process is alive. Always 200 while it runs, also while
//     it is shutting down, so a slow, orderly shutdown is never killed as
//     "dead".
//   - /readyz: its dependencies answer (the database, and for workers a
//     usable proxy), and it isn't shutting down. 200, or 503 with the names
//     of the checks that failed. Error texts are never shown: they can
//     name hosts.
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

// DefaultCheckTimeout bounds each readiness check.
const DefaultCheckTimeout = 2 * time.Second

// Health holds the readiness checks and the draining flag.
type Health struct {
	// Timeout bounds each check (DefaultCheckTimeout when zero).
	Timeout time.Duration

	mu       sync.Mutex
	checks   []check
	draining atomic.Bool
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

// Failed runs every check and returns the names of those that failed.
func (h *Health) Failed(ctx context.Context) []string {
	h.mu.Lock()
	checks := append([]check(nil), h.checks...)
	h.mu.Unlock()
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	var failed []string
	for _, c := range checks {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		err := c.fn(cctx)
		cancel()
		if err != nil {
			failed = append(failed, c.name)
		}
	}
	return failed
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
