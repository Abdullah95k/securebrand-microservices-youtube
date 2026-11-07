package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ops"
)

func get(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body %q is not JSON: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestHealthAndReadiness(t *testing.T) {
	h := &ops.Health{Timeout: 50 * time.Millisecond}
	dbUp := true
	h.Add("database", func(context.Context) error {
		if !dbUp {
			return errors.New("dial tcp fakehost:5432: connection refused")
		}
		return nil
	})
	h.Add("slow", func(ctx context.Context) error {
		<-ctx.Done() // a check that hangs is cut off by the timeout
		return ctx.Err()
	})
	mux := ops.Mux(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("yt_up 1\n")) }))

	if code, body := get(t, mux, "/healthz"); code != 200 || body["status"] != "ok" {
		t.Errorf("/healthz = %d %v", code, body)
	}
	code, body := get(t, mux, "/readyz")
	if code != 503 || body["status"] != "not ready" {
		t.Fatalf("/readyz = %d %v; the hanging check must fail", code, body)
	}
	if f, _ := body["failed"].([]any); len(f) != 1 || f[0] != "slow" {
		t.Errorf("failed = %v; want only the slow check", body["failed"])
	}

	h2 := &ops.Health{Every: -1} // checked on every request
	h2.Add("database", func(context.Context) error {
		if !dbUp {
			return errors.New("dial tcp fakehost:5432: connection refused")
		}
		return nil
	})
	mux2 := ops.Mux(h2, nil)
	if code, body := get(t, mux2, "/readyz"); code != 200 || body["status"] != "ready" {
		t.Errorf("/readyz = %d %v; want ready", code, body)
	}
	dbUp = false
	rec := httptest.NewRecorder()
	mux2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != 503 {
		t.Errorf("/readyz with the database down = %d", rec.Code)
	}
	if s := rec.Body.String(); contains(s, "fakehost") || contains(s, "refused") {
		t.Errorf("readiness must not show error texts: %q", s)
	}
	dbUp = true

	// Shutting down: not ready, but still alive.
	h2.Drain()
	if code, body := get(t, mux2, "/readyz"); code != 503 || body["status"] != "draining" {
		t.Errorf("/readyz while draining = %d %v", code, body)
	}
	if code, _ := get(t, mux2, "/healthz"); code != 200 {
		t.Errorf("/healthz while draining = %d; it must stay alive", code)
	}

	// Metrics are served when given, absent otherwise.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != 200 || rec.Body.String() != "yt_up 1\n" {
		t.Errorf("/metrics = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != 404 {
		t.Errorf("/metrics without a handler = %d", rec.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The checks run at most once a minute: in between, /readyz answers from
// the last result, a failure included. A probe that gives up first can't
// cancel a check into a false failure.
func TestReadinessIsCached(t *testing.T) {
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	calls := 0
	up := true
	h := &ops.Health{Timeout: time.Second, Now: func() time.Time { return now }}
	h.Add("database", func(ctx context.Context) error {
		calls++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond): // slower than the probe below
		}
		if !up {
			return errors.New("down")
		}
		return nil
	})
	probe := func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		return h.Failed(ctx)
	}
	if f := probe(); len(f) != 0 || calls != 1 {
		t.Fatalf("first probe: failed %v after %d calls; the probe's own timeout must not fail the check", f, calls)
	}
	up = false
	now = now.Add(59 * time.Second)
	if f := probe(); len(f) != 0 || calls != 1 {
		t.Errorf("within the minute: failed %v, %d calls; want the cached result", f, calls)
	}
	now = now.Add(2 * time.Second)
	if f := probe(); len(f) != 1 || calls != 2 {
		t.Errorf("after the minute: failed %v, %d calls", f, calls)
	}
	up = true
	now = now.Add(30 * time.Second)
	if f := probe(); len(f) != 1 || calls != 2 {
		t.Errorf("a failure is cached too: failed %v, %d calls", f, calls)
	}
}
