package innertube

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/rawstore"
)

type rawBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *rawBucket) Put(_ context.Context, key, _ string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.objects == nil {
		b.objects = map[string][]byte{}
	}
	b.objects[key] = data
	return nil
}

func (b *rawBucket) record(t *testing.T, ref string) rawstore.Record {
	t.Helper()
	b.mu.Lock()
	data, ok := b.objects[strings.TrimPrefix(ref, "s3://fake-raw/")]
	b.mu.Unlock()
	if !ok {
		t.Fatalf("no raw object for %s", ref)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	var r rawstore.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (b *rawBucket) count(prefix string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for k := range b.objects {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// Every answer read is kept raw (REQ-010 fix 8): ordinary ones under
// yts/30d/, blocks under yts/keep/; images are media and aren't.
func TestRawResponsesKept(t *testing.T) {
	r := newRig(t, 2)
	bucket := &rawBucket{}
	raw := rawstore.New(bucket, rawstore.Options{Sample: 1e-12, RefBase: "s3://fake-raw/", ParserVersion: "2026.11.06.1"})
	stop := raw.Start(5 * time.Second)
	r.client.opt.Raw = raw
	r.client.opt.CDNBase = r.yt.URL
	if _, err := r.client.DetectClientVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.yt.BlockNext("browse", 1, fakeyt.Block429)
	resp, err := r.client.Call(context.Background(), Browse, map[string]any{"browseId": "UCfakeRAWAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.client.Thumbnail(context.Background(), "fakevid0001", "hqdefault"); err != nil {
		t.Fatal(err)
	}
	stop()
	if !strings.HasPrefix(resp.RawRef, "s3://fake-raw/yts/30d/") {
		t.Fatalf("RawRef = %q", resp.RawRef)
	}
	got := bucket.record(t, resp.RawRef)
	if got.Endpoint != "browse" || got.Method != "POST" || got.Status != 200 || got.ParserVersion != "2026.11.06.1" ||
		got.Proxy != resp.Proxy || !strings.Contains(got.Body, "UCfakeRAWAAAAAAAAAAAAAAA") || got.Kept != "" {
		t.Errorf("record = %+v", got)
	}
	if n := bucket.count("yts/keep/"); n != 1 {
		t.Errorf("kept forever: %d objects; want the blocked answer", n)
	}
	// Pages (the homepage read for the client version and the visitor ID)
	// are kept too; the thumbnail is not.
	for k := range bucket.objects {
		if strings.Contains(k, "/cdn/") || strings.Contains(k, ".jpg") {
			t.Errorf("an image was kept raw: %s", k)
		}
	}
	if bucket.count("yts/30d/") < 2 {
		t.Errorf("30-day objects = %d; want the answer and the homepage", bucket.count("yts/30d/"))
	}
	for k := range bucket.objects {
		if strings.HasPrefix(k, "yts/keep/") {
			if rec := bucket.record(t, "s3://fake-raw/"+k); rec.Status != 429 || !strings.HasPrefix(rec.Kept, "failure: ") {
				t.Errorf("kept record = %+v", rec)
			}
		}
	}
}
