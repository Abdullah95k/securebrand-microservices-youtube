package collect_test

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

	"github.com/abdullah95k/securebrand-contracts/conformance"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/contract"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/rawstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
)

type bucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *bucket) Put(_ context.Context, key, _ string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.objects == nil {
		b.objects = map[string][]byte{}
	}
	b.objects[key] = data
	return nil
}

// A page the parsers can't read is kept forever, and parse_failures
// points to it (REQ-010 fix 8).
func TestParseFailureKeepsTheRawPage(t *testing.T) {
	r := newRig(t, []fakeyt.FakeChannel{fakeChannel()})
	ctx := context.Background()
	b := &bucket{}
	raw := rawstore.New(b, rawstore.Options{Sample: 1e-12, RefBase: "s3://fake-raw/", ParserVersion: "2026.11.06.1"})
	stop := raw.Start(5 * time.Second)
	r.svc.Raw = raw
	r.svc.Fetch.Client = innertube.New(innertube.Options{Pool: r.pp, BaseURL: r.yt.URL, CDNBase: r.yt.URL, Raw: raw})
	// The channel page comes back with its ID but no title and no tabs.
	r.yt.Handle("browse", func(map[string]any) (int, any) {
		return 200, map[string]any{"metadata": map[string]any{"channelMetadataRenderer": map[string]any{"externalId": chID}}}
	})
	if err := registry.List(ctx, r.pool, nil, chID, registry.Input{Input: chID, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	r.work(t, 1500*time.Millisecond)
	stop()

	var ref string
	if err := r.pool.QueryRow(ctx, `SELECT raw_ref FROM parse_failures WHERE endpoint = 'browse:channel' AND raw_ref IS NOT NULL LIMIT 1`).Scan(&ref); err != nil {
		t.Fatalf("no parse failure with a raw_ref: %v", err)
	}
	found := false
	for _, one := range strings.Fields(ref) {
		if !strings.HasPrefix(one, "s3://fake-raw/yts/keep/") {
			t.Errorf("raw_ref %s is not kept forever", one)
			continue
		}
		b.mu.Lock()
		data, ok := b.objects[strings.TrimPrefix(one, "s3://fake-raw/")]
		b.mu.Unlock()
		if !ok {
			t.Errorf("raw_ref %s has no object", one)
			continue
		}
		zr, _ := gzip.NewReader(bytes.NewReader(data))
		body, _ := io.ReadAll(zr)
		var rec rawstore.Record
		if err := json.Unmarshal(body, &rec); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(rec.Kept, "failure: parse failure: browse:channel") && strings.Contains(rec.Body, chID) {
			found = true
		}
	}
	if !found {
		t.Errorf("raw_ref %q doesn't point to the channel page kept with its reason", ref)
	}

	// The channel is stored all the same, with no name: its event is a
	// partial account the contract takes (11 November: 3 were refused).
	rows, err := outbox.Unsent(ctx, r.pool, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	m := &contract.Mapper{Options: conformance.Options{Production: true}}
	accounts := 0
	for _, row := range rows {
		if row.Kind != outbox.KindChannel || row.EntityID != chID {
			continue
		}
		msgs, err := m.Map(row)
		if err != nil {
			t.Fatalf("the nameless channel's event: %v", err)
		}
		for _, msg := range msgs {
			if a, ok := msg.Msg.(*contentv1.Account); ok {
				accounts++
				if !a.GetEnvelope().GetPartial() || a.GetDisplayName() != "" {
					t.Errorf("nameless channel account %v", a)
				}
			}
		}
	}
	if accounts == 0 {
		t.Error("no account event for the nameless channel")
	}
}
