package fbdisc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fbdisc"
)

func TestFind(t *testing.T) {
	links := []string{"facebook.com/fakeIraqPage", "instagram.com/fake", "https://m.facebook.com/fakePageTwo/", "facebook.com/fakeIraqPage"}
	desc := "Follow us: https://www.facebook.com/fakePageThree. Also fb.me/fakeShort, notfacebook.com/x and facebook.com/ alone."
	got := fbdisc.Find(links, desc)
	want := []string{"facebook.com/fakeIraqPage", "https://m.facebook.com/fakePageTwo/", "https://www.facebook.com/fakePageThree", "fb.me/fakeShort"}
	if !slices.Equal(got, want) {
		t.Errorf("Find = %q\nwant %q", got, want)
	}
	if got := fbdisc.Find(nil, "no links here, youtube.com/@fake"); len(got) != 0 {
		t.Errorf("no Facebook link: %q", got)
	}
}

// fakeFBS is the Facebook collector's candidates endpoint.
type fakeFBS struct {
	mu     sync.Mutex
	status int
	got    []map[string]string
	auth   []string
}

func (f *fakeFBS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != fbdisc.Path {
		http.NotFound(w, r)
		return
	}
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.got = append(f.got, body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"result":"queued"}`))
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Listed channels' links go to fbs once each, within the day's limit; other
// channels (candidates) are never read, nor commenters (only the channels
// table is); a 503 pauses for an hour.
func TestRunSendsEachLinkOnce(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	for _, c := range []struct{ id, status, links, desc string }{
		{"UCfakeFBLISTED0000000001", "listed", `[{"Title":"Facebook","URL":"facebook.com/fakeIraqPage"}]`, "fan page facebook.com/fakeIraqPage"},
		{"UCfakeFBLISTED0000000002", "listed", `[]`, "https://www.facebook.com/fakePageTwo and fb.com/fakeThree"},
		{"UCfakeFBCANDIDATE0000001", "candidate", `[{"Title":"f","URL":"facebook.com/fakeCandidate"}]`, ""},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO channels (id, status, links, description) VALUES ($1, $2, $3::jsonb, $4)`,
			c.id, c.status, c.links, c.desc); err != nil {
			t.Fatal(err)
		}
	}
	fbs := &fakeFBS{}
	srv := httptest.NewServer(fbs)
	defer srv.Close()
	now := time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)
	s := &fbdisc.Sender{Pool: pool, URL: srv.URL + "/", Key: "fake-key", Daily: 2, Now: func() time.Time { return now }}

	st, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Found != 3 || st.Sent != 2 || len(fbs.got) != 2 {
		t.Fatalf("found %d, sent %d, requests %d; want 3 found, 2 sent (the day's limit)", st.Found, st.Sent, len(fbs.got))
	}
	first := fbs.got[0]
	if first["ref"] != "facebook.com/fakeIraqPage" || first["source"] != "yts" ||
		first["evidence_url"] != "https://www.youtube.com/channel/UCfakeFBLISTED0000000001/about" || len(first) != 3 {
		t.Errorf("request body %v", first)
	}
	if fbs.auth[0] != "Bearer fake-key" {
		t.Error("the key goes in the Authorization header")
	}
	if n := count(t, pool, `SELECT count(*) FROM fb_candidates WHERE result = 'queued'`); n != 2 {
		t.Errorf("%d links recorded as queued", n)
	}

	// Next day: the third link goes; nothing is sent twice.
	now = now.Add(24 * time.Hour)
	if st, err := s.Run(ctx); err != nil || st.Sent != 1 || st.Found != 0 {
		t.Fatalf("next day: %+v %v", st, err)
	}
	if st, err := s.Run(ctx); err != nil || st.Sent != 0 {
		t.Fatalf("nothing left to send: %+v %v", st, err)
	}
	for _, b := range fbs.got {
		if b["ref"] == "facebook.com/fakeCandidate" {
			t.Errorf("only listed channels' links are sent: %v", b)
		}
	}

	// A new link while fbs's mentions are off: kept, and yts waits an hour.
	if _, err := pool.Exec(ctx, `UPDATE channels SET description = 'facebook.com/fakeNewPage' WHERE id = 'UCfakeFBLISTED0000000001'`); err != nil {
		t.Fatal(err)
	}
	fbs.status = http.StatusServiceUnavailable
	if st, err := s.Run(ctx); err != nil || !st.Paused || st.Sent != 0 || st.Found != 1 {
		t.Fatalf("503: %+v %v", st, err)
	}
	requests := len(fbs.got)
	now = now.Add(30 * time.Minute)
	if st, _ := s.Run(ctx); !st.Paused || len(fbs.got) != requests {
		t.Error("within the hour, no request")
	}
	fbs.status = 0
	now = now.Add(31 * time.Minute)
	if st, err := s.Run(ctx); err != nil || st.Sent != 1 {
		t.Fatalf("after the pause: %+v %v", st, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM fb_candidates WHERE sent_at IS NULL`); n != 0 {
		t.Errorf("%d links left unsent", n)
	}
}
