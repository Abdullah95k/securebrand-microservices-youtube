package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/erasure"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/webhook"
)

const (
	ch     = "UCfakeWEBHOOKCHANNEL0001"
	ch2    = "UCfakeWEBHOOKCHANNEL0002"
	secret = "test-webhook-secret-0123456789abcdef"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := webhook.Load(filepath.Join(dir, "none.json")); err != webhook.ErrNotConfigured {
		t.Errorf("no file: %v", err)
	}
	sf := filepath.Join(dir, "secret")
	_ = os.WriteFile(sf, []byte(secret+"\n"), 0o600)
	short := filepath.Join(dir, "short")
	_ = os.WriteFile(short, []byte("abc\n"), 0o600)
	cfg := func(url, events, secretFile string) string {
		p := filepath.Join(dir, "c.json")
		_ = os.WriteFile(p, []byte(`{"endpoints": [{"name": "fbs", "url": "`+url+`", "secret_file": "`+secretFile+`", "events": [`+events+`]}]}`), 0o600)
		return p
	}
	if c, err := webhook.Load(cfg("https://hooks.example/yt?token=x", `"video.relevant"`, sf)); err != nil || len(c.Endpoints) != 1 {
		t.Errorf("valid file: %v", err)
	}
	for name, p := range map[string]string{
		"plain http":    cfg("http://hooks.example/yt", `"video.relevant"`, sf),
		"unknown event": cfg("https://hooks.example/yt", `"video.deleted"`, sf),
		"no events":     cfg("https://hooks.example/yt", ``, sf),
		"short secret":  cfg("https://hooks.example/yt", `"video.relevant"`, short),
	} {
		if _, err := webhook.Load(p); err == nil {
			t.Errorf("%s must be refused", name)
		} else if strings.Contains(err.Error(), "token=x") {
			t.Errorf("errors must not show the URL: %v", err)
		}
	}
}

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"id":1}`)
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("X-SecureBrand-Timestamp", "1800000000")
	h.Set("X-SecureBrand-Signature", webhook.Sign([]byte(secret), now.Unix(), body))
	if err := webhook.Verify([]byte(secret), h, body, now.Add(time.Minute)); err != nil {
		t.Error(err)
	}
	if err := webhook.Verify([]byte(secret), h, []byte(`{"id":2}`), now); err == nil {
		t.Error("a changed body must fail")
	}
	if err := webhook.Verify([]byte(secret), h, body, now.Add(10*time.Minute)); err == nil {
		t.Error("an old request (a replay) must fail")
	}
	if webhook.Backoff(1) != 30*time.Second || webhook.Backoff(3) != 2*time.Minute || webhook.Backoff(20) != 6*time.Hour {
		t.Error("backoff 30 s, doubling, at most 6 h")
	}
}

// receiver is a test endpoint.
type receiver struct {
	mu     sync.Mutex
	got    []webhook.Body
	fail   bool
	badSig int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if webhook.Verify([]byte(secret), req.Header, body, time.Now()) != nil {
		r.badSig++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var b webhook.Body
	_ = json.Unmarshal(body, &b)
	if req.Header.Get("X-SecureBrand-Event") != b.Type {
		r.badSig++
	}
	r.got = append(r.got, b)
}

func TestOutbox(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	rcv := &receiver{}
	srv := httptest.NewServer(rcv)
	defer srv.Close()
	d := &webhook.Dispatcher{Pool: pool, Log: quiet, Cfg: webhook.Config{Endpoints: []webhook.Endpoint{
		webhook.Endpoint{Name: "fbs", URL: srv.URL + "/hook", Events: []string{webhook.VideoRelevant, webhook.ChannelListed}}.WithSecret(secret),
	}}}

	// An event from before the endpoint existed is not sent.
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Enqueue(ctx); err != nil || n != 0 {
		t.Fatalf("first enqueue: %d %v", n, err)
	}

	// Triggers record what becomes relevant or listed.
	for _, sql := range []string{
		`INSERT INTO videos (id, channel_id, kind, title, relevant, matched_keywords) VALUES ('fakeWv1', '` + ch + `', 'video', 'not yet', false, '{}')`,
		`UPDATE videos SET relevant = true, title = 'بغداد', matched_keywords = '{baghdad}' WHERE id = 'fakeWv1'`,
		`UPDATE videos SET relevant = true WHERE id = 'fakeWv1'`, // still relevant: no second event
		`INSERT INTO posts (id, channel_id, text, relevant, matched_keywords) VALUES ('UgkxFAKEWEBHOOK', '` + ch + `', 'العراق', true, '{iraq}')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := registry.List(ctx, pool, nil, ch2, registry.Input{Input: ch2, Tier: policy.C, AddedBy: "discovery"}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM webhook_events WHERE type = 'video.relevant'`); n != 1 {
		t.Errorf("%d video events; want one when it became relevant", n)
	}
	if n, err := d.Enqueue(ctx); err != nil || n != 2 {
		t.Fatalf("enqueue: %d %v (want the video and the new channel; posts are not subscribed)", n, err)
	}
	if n, _ := d.Enqueue(ctx); n != 0 {
		t.Error("enqueue twice creates nothing new")
	}
	if n, err := d.Deliver(ctx, 10); err != nil || n != 2 {
		t.Fatalf("deliver: %d %v", n, err)
	}
	if rcv.badSig != 0 || len(rcv.got) != 2 {
		t.Fatalf("received %d, bad signatures %d", len(rcv.got), rcv.badSig)
	}
	var video map[string]any
	for _, b := range rcv.got {
		if b.Type == webhook.VideoRelevant {
			_ = json.Unmarshal(b.Data, &video)
		}
	}
	if video["video_id"] != "fakeWv1" || video["title"] != "بغداد" || video["url"] != "https://www.youtube.com/watch?v=fakeWv1" {
		t.Errorf("video payload %v", video)
	}

	// Failures are retried with backoff, then dead; retry queues them again.
	rcv.fail = true
	if _, err := pool.Exec(ctx, `UPDATE videos SET relevant = false WHERE id = 'fakeWv1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE videos SET relevant = true WHERE id = 'fakeWv1'`); err != nil {
		t.Fatal(err)
	}
	_, _ = d.Enqueue(ctx)
	if n, _ := d.Deliver(ctx, 10); n != 0 {
		t.Error("a 500 is not a delivery")
	}
	if n := count(t, pool, `SELECT count(*) FROM webhook_deliveries WHERE status = 'pending' AND attempts = 1
		AND last_status = 500 AND next_at > now() + interval '20 seconds'`); n != 1 {
		t.Error("a failed delivery waits 30 seconds")
	}
	_, _ = pool.Exec(ctx, `UPDATE webhook_deliveries SET next_at = now(), attempts = $1 WHERE status = 'pending'`, webhook.MaxAttempts-1)
	_, _ = d.Deliver(ctx, 10)
	if n := count(t, pool, `SELECT count(*) FROM webhook_deliveries WHERE status = 'dead'`); n != 1 {
		t.Error("after the last attempt a delivery is dead")
	}
	rcv.fail = false
	if n, err := webhook.Retry(ctx, pool, "fbs"); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if n, _ := d.Deliver(ctx, 10); n != 1 {
		t.Error("a retried delivery goes out")
	}
	counts, _ := webhook.Counts(ctx, pool)
	if len(counts) != 1 || counts[0].Status != "delivered" || counts[0].N != 3 {
		t.Errorf("counts %+v", counts)
	}
	if st, err := d.Test(ctx, "fbs"); err != nil || st != 200 {
		t.Errorf("test ping: %d %v", st, err)
	}

	// Erasing the video deletes its events (pending ones are never sent)
	// and records an erasure event; its ID is removed after delivery.
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	er := &erasure.Service{Pool: pool, Keyer: k, Sup: privacy.NewSuppressor(k), Log: quiet, Webhooks: true}
	id, err := er.Request(ctx, privacy.Video, "fakeWv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := er.Handlers()[erasure.KindErasure](ctx, queue.Job{Kind: erasure.KindErasure, EntityID: itoa(id), Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM webhook_events WHERE entity_id = 'fakeWv1'`); n != 0 {
		t.Error("the erased video's events must be gone")
	}
	if n := count(t, pool, `SELECT count(*) FROM webhook_events WHERE type = 'erasure' AND payload->>'id' = 'fakeWv1'`); n != 1 {
		t.Fatal("an erasure event tells receivers what to delete")
	}
	_, _ = pool.Exec(ctx, `UPDATE webhook_events SET created_at = now() - interval '11 minutes' WHERE type = 'erasure'`)
	_, _ = d.Deliver(ctx, 10) // fbs does not take erasure events
	if n := count(t, pool, `SELECT count(*) FROM webhook_events WHERE type = 'erasure' AND payload ? 'id'`); n != 0 {
		t.Error("an erasure event keeps no raw ID once no delivery is pending")
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
