package websub_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/detect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/websub"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
)

const ch = "UCfakeWEBSUB000000000001"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSubscribeVerifyAndNotify(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	_, _ = kwstore.ImportSeed(ctx, pool)
	live := &kwstore.Live{}
	_, _ = live.Refresh(ctx, pool)
	_ = registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B})

	// A fake hub records the subscription request.
	var mu sync.Mutex
	var got url.Values
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		got = r.PostForm
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hub.Close()
	cfg := websub.Config{Callback: "https://push.example.org/websub", Secret: []byte("server-secret-0123456789abcdef-0123456789"), Hub: hub.URL}
	sub := &websub.Subscriber{Pool: pool, Cfg: cfg, Log: quiet}
	if n, err := sub.Ensure(ctx); err != nil || n != 1 {
		t.Fatalf("ensure: %d, err %v", n, err)
	}
	next, err := sub.Handlers()[queue.KindWebSub](ctx, queue.Job{Kind: queue.KindWebSub, EntityID: ch})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(next); d < 8*24*time.Hour || d > 9*24*time.Hour+time.Hour {
		t.Errorf("renewal in %v; want 9 days (a day before the 10-day lease ends)", d)
	}
	mu.Lock()
	secret := got.Get("hub.secret")
	if got.Get("hub.mode") != "subscribe" || got.Get("hub.topic") != websub.TopicURL(ch) ||
		!strings.Contains(got.Get("hub.callback"), "channel="+ch) || len(secret) != 64 || strings.Contains(secret, "server-secret") {
		t.Errorf("subscription request %v", got)
	}
	mu.Unlock()

	rc := &websub.Receiver{Pool: pool, Cfg: cfg, Log: quiet,
		RSS: &detect.RSS{Pool: pool, Write: &writer.Writer{Pool: pool, KW: live}, Log: quiet}}
	srv := httptest.NewServer(rc)
	defer srv.Close()

	// The hub's verification: echo the challenge for a listed channel only.
	verify := func(channel, mode string) (int, string) {
		u := srv.URL + "?channel=" + channel + "&hub.mode=" + mode + "&hub.challenge=xyz&hub.topic=" + url.QueryEscape(websub.TopicURL(channel))
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := verify(ch, "subscribe"); code != 200 || body != "xyz" {
		t.Errorf("verify: %d %q", code, body)
	}
	if code, _ := verify("UCfakeNOTLISTED000000001", "subscribe"); code != 404 {
		t.Errorf("a channel we do not list must not be confirmed (%d)", code)
	}

	// A signed notification queues the video at P0; a forged one is dropped.
	body := fakeyt.FeedXML(ch, []fakeyt.FeedEntry{{VideoID: "fakeWv1", ChannelID: ch, Title: "new upload", Published: time.Now()}})
	post := func(sig string, body []byte) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"?channel="+ch, strings.NewReader(string(body)))
		req.Header.Set("X-Hub-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	forged := fakeyt.FeedXML(ch, []fakeyt.FeedEntry{{VideoID: "fakeWforged", ChannelID: ch, Title: "x", Published: time.Now()}})
	if code := post("sha1=00", forged); code != 204 {
		t.Errorf("forged: %d", code)
	}
	m := hmac.New(sha1.New, []byte(secret))
	m.Write(body)
	if code := post("sha1="+hex.EncodeToString(m.Sum(nil)), body); code != 204 {
		t.Errorf("signed: %d", code)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM schedule WHERE kind = 'video' AND entity_id = 'fakeWv1' AND priority = 0`).Scan(&n)
	if n != 1 {
		t.Error("a signed notification must queue the video at P0")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM videos WHERE id = 'fakeWforged'`).Scan(&n)
	if n != 0 {
		t.Error("a forged notification must be dropped")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM videos WHERE id = 'fakeWv1' AND source = 'websub'`).Scan(&n)
	if n != 1 {
		t.Error("the video's source must be websub")
	}

	// Unlisted: the next run unsubscribes and ends the job.
	_ = registry.Unlist(ctx, pool, ch, "test")
	next, err = sub.Handlers()[queue.KindWebSub](ctx, queue.Job{Kind: queue.KindWebSub, EntityID: ch})
	mu.Lock()
	mode := got.Get("hub.mode")
	mu.Unlock()
	if err != nil || !next.IsZero() || mode != "unsubscribe" {
		t.Errorf("after unlisting: next %v, err %v, mode %q", next, err, mode)
	}
}

func TestDisabledWithoutCallback(t *testing.T) {
	pool := dbtest.New(t)
	sub := &websub.Subscriber{Pool: pool, Cfg: websub.Config{}, Log: quiet}
	if n, err := sub.Ensure(context.Background()); n != 0 || err != nil {
		t.Errorf("push off: ensure added %d (err %v)", n, err)
	}
}
