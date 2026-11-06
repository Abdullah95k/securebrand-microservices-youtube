package rawstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// memory is an in-memory object store.
type memory struct {
	mu      sync.Mutex
	objects map[string][]byte
	fail    int // fail this many Puts first
	puts    int
}

func (m *memory) Put(_ context.Context, key, contentType string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if m.fail > 0 {
		m.fail--
		return errors.New("bucket unavailable")
	}
	if contentType != "application/gzip" {
		return errors.New("wrong content type " + contentType)
	}
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	m.objects[key] = data
	return nil
}

func (m *memory) get(t *testing.T, key string) Record {
	t.Helper()
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		t.Fatalf("no object %s", key)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (m *memory) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		out = append(out, k)
	}
	return out
}

type counts struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *counts) hook(class, result string, _ int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[class+"/"+result]++
}

func (c *counts) get(k string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[k]
}

var at = time.Date(2026, 11, 6, 21, 30, 15, 0, time.UTC)

func rec(endpoint string) Record {
	return Record{At: at, Endpoint: endpoint, Method: "POST", URL: "https://www.youtube.com/youtubei/v1/" + endpoint + "?prettyPrint=false",
		Status: 200, Proxy: "px-fake01"}
}

func TestCaptureClasses(t *testing.T) {
	m := &memory{}
	var c counts
	s := New(m, Options{Sample: 1e-12, RefBase: "s3://fake-raw/", ParserVersion: "2026.11.06.1", OnResult: c.hook})
	stop := s.Start(5 * time.Second)
	ctx := context.Background()
	body := []byte(`{"title":"fake title"}`)

	ordinary := s.Capture(ctx, rec("browse"), body, "")
	failed := s.Capture(ctx, rec("next"), []byte("<html>sorry</html>"), "innertube: block signal from YouTube")
	binary := s.Capture(ctx, rec("navigation/resolve_url"), []byte{0xff, 0xfe, 0x00}, "")
	stop()

	if !strings.HasPrefix(ordinary, "s3://fake-raw/yts/30d/2026/11/06/browse/213015.000000-") || !strings.HasSuffix(ordinary, ".json.gz") {
		t.Errorf("ordinary ref = %s", ordinary)
	}
	if !strings.HasPrefix(failed, "s3://fake-raw/yts/keep/2026/11/06/next/") {
		t.Errorf("failure ref = %s", failed)
	}
	r := m.get(t, strings.TrimPrefix(ordinary, "s3://fake-raw/"))
	if r.V != 1 || r.Collector != "yts" || r.ParserVersion != "2026.11.06.1" || r.Body != string(body) || r.Kept != "" ||
		r.Proxy != "px-fake01" || r.Status != 200 || !r.At.Equal(at) {
		t.Errorf("ordinary record = %+v", r)
	}
	f := m.get(t, strings.TrimPrefix(failed, "s3://fake-raw/"))
	if f.Kept != "failure: innertube: block signal from YouTube" || f.Body != "<html>sorry</html>" {
		t.Errorf("failure record = %+v", f)
	}
	b := m.get(t, strings.TrimPrefix(binary, "s3://fake-raw/"))
	if b.Body != "" || !bytes.Equal(b.BodyBase64, []byte{0xff, 0xfe, 0x00}) {
		t.Errorf("a body that isn't UTF-8 must be kept exactly: %+v", b)
	}
	if !strings.Contains(binary, "/navigation_resolve_url/") {
		t.Errorf("endpoint in key = %s", binary)
	}
	if c.get("keep/stored") != 1 || c.get("30d/stored") != 2 {
		t.Errorf("counts = %v", c.n)
	}
}

func TestSampleAndSkipExpiring(t *testing.T) {
	m := &memory{}
	s := New(m, Options{Sample: 1, RefBase: "s3://fake-raw/"})
	stop := s.Start(5 * time.Second)
	ref := s.Capture(context.Background(), rec("browse"), []byte(`{}`), "")
	stop()
	if !strings.Contains(ref, "/yts/keep/") || m.get(t, strings.TrimPrefix(ref, "s3://fake-raw/")).Kept != "sample" {
		t.Errorf("a sampled answer must be kept forever: %s", ref)
	}

	// About 1% of ordinary answers are sampled.
	s = New(&memory{}, Options{Sample: 0.01})
	n := 0
	for i := 0; i < 20000; i++ {
		if s.sampled() {
			n++
		}
	}
	if n < 120 || n > 290 {
		t.Errorf("sampled %d of 20,000; want about 200", n)
	}

	// With the 30-day rest off, only failures and the sample are written.
	m = &memory{}
	s = New(m, Options{Sample: 1e-12, SkipExpiring: true})
	stop = s.Start(5 * time.Second)
	if ref := s.Capture(context.Background(), rec("browse"), []byte(`{}`), ""); ref != "" {
		t.Errorf("ref = %q; want none", ref)
	}
	if ref := s.Capture(context.Background(), rec("browse"), []byte(`{}`), "HTTP 500"); ref == "" {
		t.Error("a failure must still be kept")
	}
	stop()
	if len(m.keys()) != 1 {
		t.Errorf("objects = %v", m.keys())
	}
}

func TestQueueFullAndRetries(t *testing.T) {
	// No uploads running and room for one: a second 30-day file is
	// dropped and counted; a failure waits, then is dropped too.
	var c counts
	s := New(&memory{}, Options{Sample: 1e-12, Queue: 1, OnResult: c.hook})
	ctx := context.Background()
	if ref := s.Capture(ctx, rec("browse"), []byte(`{}`), ""); ref == "" {
		t.Fatal("the first must be queued")
	}
	if ref := s.Capture(ctx, rec("browse"), []byte(`{}`), ""); ref != "" || c.get("30d/dropped") != 1 {
		t.Errorf("ref %q, counts %v; want dropped", ref, c.n)
	}
	start := time.Now()
	if ref := s.Capture(ctx, rec("browse"), []byte(`{}`), "HTTP 500"); ref != "" || c.get("keep/dropped") != 1 {
		t.Errorf("ref %q, counts %v; want dropped after a wait", ref, c.n)
	}
	if time.Since(start) < time.Second {
		t.Error("a failure should wait for room before it is dropped")
	}

	// A bucket that fails twice is tried a third time; one that keeps
	// failing is counted.
	m := &memory{fail: 2}
	c = counts{}
	s = New(m, Options{Sample: 1e-12, RetryWait: time.Millisecond, Workers: 1, OnResult: c.hook})
	stop := s.Start(5 * time.Second)
	s.Capture(ctx, rec("browse"), []byte(`{}`), "")
	stop()
	if m.puts != 3 || c.get("30d/stored") != 1 {
		t.Errorf("puts %d, counts %v; want stored on the third try", m.puts, c.n)
	}
	m = &memory{fail: 10}
	c = counts{}
	s = New(m, Options{Sample: 1e-12, RetryWait: time.Millisecond, Workers: 1, OnResult: c.hook})
	stop = s.Start(5 * time.Second)
	s.Capture(ctx, rec("browse"), []byte(`{}`), "")
	stop()
	if m.puts != 3 || c.get("30d/failed") != 1 {
		t.Errorf("puts %d, counts %v; want 3 tries then failed", m.puts, c.n)
	}
}

func TestKeepRecent(t *testing.T) {
	m := &memory{}
	s := New(m, Options{Sample: 1e-12, RefBase: "s3://fake-raw/"})
	stop := s.Start(5 * time.Second)
	ctx, job := WithJob(context.Background())
	var refs []string
	for i := 0; i < 6; i++ {
		failure := ""
		if i == 5 {
			failure = "HTTP 500"
		}
		refs = append(refs, s.Capture(ctx, rec("browse"), []byte(`{"n":`+string(rune('0'+i))+`}`), failure))
	}
	if got := job.Recent(); len(got) != recentPerJob || got[recentPerJob-1].Ref != refs[5] {
		t.Fatalf("recent = %d, last %s", len(got), got[len(got)-1].Ref)
	}
	kept := s.KeepRecent(ctx, "parse failure: browse:channel: no title")
	stop()
	// The three 30-day answers are stored again under keep/; the one
	// already kept is referred to as it is.
	if len(kept) != 4 || kept[3] != refs[5] {
		t.Fatalf("kept = %v", kept)
	}
	for _, ref := range kept[:3] {
		if !strings.Contains(ref, "/yts/keep/") {
			t.Errorf("ref %s not under keep/", ref)
		}
		r := m.get(t, strings.TrimPrefix(ref, "s3://fake-raw/"))
		if r.Kept != "failure: parse failure: browse:channel: no title" || !strings.HasPrefix(r.Body, `{"n":`) {
			t.Errorf("kept record = %+v", r)
		}
	}
	// The originals under 30d/ stay (the bucket expires them).
	if len(m.keys()) != 6+3 {
		t.Errorf("objects = %d; want 9", len(m.keys()))
	}
}

func TestNilStoreAndJob(t *testing.T) {
	var s *Store
	ctx := context.Background()
	if ref := s.Capture(ctx, rec("browse"), []byte(`{}`), "x"); ref != "" {
		t.Errorf("ref = %q", ref)
	}
	if refs := s.KeepRecent(ctx, "x"); refs != nil {
		t.Errorf("refs = %v", refs)
	}
	s.Start(time.Second)()
	var j *Job
	j.add("r", Record{}, nil, ClassExpire)
	if j.Recent() != nil || JobFrom(ctx) != nil {
		t.Error("a missing job remembers nothing")
	}
}

func TestKey(t *testing.T) {
	k := Key(ClassKeep, time.Date(2026, 1, 2, 3, 4, 5, 6000, time.FixedZone("Iraq", 3*3600)), "Navigation/Resolve_URL")
	if !strings.HasPrefix(k, "yts/keep/2026/01/02/navigation_resolve_url/000405.000006-") {
		t.Errorf("key = %s (UTC date and time, safe endpoint)", k)
	}
	if Key(ClassExpire, at, "") == Key(ClassExpire, at, "") {
		t.Error("two keys at the same moment must differ")
	}
	if !strings.Contains(Key(ClassExpire, at, ""), "/page/") {
		t.Error("an empty endpoint is a page")
	}
}
