package ytapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "test-key-not-real"

func fakeAPI(t *testing.T) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		if r.Header.Get("X-Goog-Api-Key") != testKey {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"errors":[{"reason":"keyInvalid"}]}}`))
			return
		}
		switch r.URL.Path {
		case "/search":
			w.Write([]byte(`{"nextPageToken":"P2","pageInfo":{"totalResults":2},"items":[
			  {"id":{"videoId":"fakevid0001"},"snippet":{"channelId":"UCfakeCHANNELCHANNEL0001","channelTitle":"c","title":"بغداد","publishedAt":"2026-10-31T10:00:00Z"}}]}`))
		case "/videos":
			w.Write([]byte(`{"items":[{"id":"fakevid0001","snippet":{"publishedAt":"2026-10-31T10:00:00Z","categoryId":"25","tags":["a","b"],"channelId":"UCfakeCHANNELCHANNEL0001"},
			  "statistics":{"viewCount":"12345","likeCount":"678"},"contentDetails":{"duration":"PT12M34S"}}]}`))
		case "/videoCategories":
			w.Write([]byte(`{"items":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestSearchSendsKeyInHeaderOnly(t *testing.T) {
	srv, seen := fakeAPI(t)
	l, _ := NewLedger("", DefaultDailyQuota)
	c := New(testKey, srv.URL, l)
	res, err := c.Search(context.Background(), "بغداد|البصرة", SearchOptions{RegionCode: "IQ", RelevanceLanguage: "ar", PublishedAfter: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ChannelID != "UCfakeCHANNELCHANNEL0001" || res.NextPageToken != "P2" {
		t.Errorf("result = %+v", res)
	}
	r := (*seen)[0]
	if strings.Contains(r.URL.RawQuery, testKey) {
		t.Error("the API key must never be in the URL")
	}
	q := r.URL.Query()
	if q.Get("order") != "date" || q.Get("type") != "video" || q.Get("regionCode") != "IQ" || q.Get("q") != "بغداد|البصرة" || q.Get("publishedAfter") != "2026-10-31T00:00:00Z" {
		t.Errorf("query = %v", q)
	}
	if _, total := l.Used(time.Now()); total != CostSearch {
		t.Errorf("ledger total = %d, want %d", total, CostSearch)
	}
}

func TestVideosListAndErrors(t *testing.T) {
	srv, _ := fakeAPI(t)
	l, _ := NewLedger("", DefaultDailyQuota)
	vs, err := New(testKey, srv.URL, l).VideosList(context.Background(), []string{"fakevid0001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Views != 12345 || !vs[0].HasLikes || vs[0].Likes != 678 || vs[0].HasComments || vs[0].TagCount != 2 {
		t.Errorf("stats = %+v", vs)
	}
	_, err = New("wrong", srv.URL, l).VideosList(context.Background(), []string{"x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "keyInvalid" || strings.Contains(err.Error(), "wrong") {
		t.Errorf("err = %v", err)
	}
	if _, err := New(testKey, srv.URL, l).VideosList(context.Background(), make([]string, 51)); err == nil {
		t.Error("want error for more than 50 IDs")
	}
}

func TestLedgerCeilingAndPacificReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	l, err := NewLedger(path, 250)
	if err != nil {
		t.Fatal(err)
	}
	// 09:00 Iraq time (UTC+3) on 2 Nov 2026 is 23:00 on 1 Nov in Los Angeles (PST).
	before := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)
	if err := l.Charge(before, "search", 100); err != nil {
		t.Fatal(err)
	}
	if err := l.Charge(before, "search", 100); err != nil {
		t.Fatal(err)
	}
	if err := l.Charge(before, "search", 100); !errors.Is(err, ErrQuota) {
		t.Fatalf("err = %v, want ErrQuota at the ceiling", err)
	}
	// The count survives a restart.
	l2, err := NewLedger(path, 250)
	if err != nil {
		t.Fatal(err)
	}
	if _, total := l2.Used(before); total != 200 {
		t.Errorf("reloaded total = %d", total)
	}
	// 11:00 Iraq time = midnight Pacific (PST): a new quota day.
	after := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	if err := l2.Charge(after, "search", 100); err != nil {
		t.Fatalf("after the Pacific midnight reset: %v", err)
	}
	if PacificDay(before) == PacificDay(after) {
		t.Error("quota days should differ across Pacific midnight")
	}
}

func TestCheckKey(t *testing.T) {
	srv, _ := fakeAPI(t)
	l, _ := NewLedger("", DefaultDailyQuota)
	if err := New(testKey, srv.URL, l).CheckKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if used, _ := l.Used(time.Now()); used["videoCategories"] != 1 {
		t.Errorf("check should cost 1 unit, used %v", used)
	}
}
