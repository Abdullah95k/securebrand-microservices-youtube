package relay_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-contracts/conformance"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	ytv1 "github.com/Abdullah95k/securebrand-microservices-youtube/gen/go/securebrand/yt/v1"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collecttest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/contract"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/relay"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// kafka is an in-memory Kafka with every topic the relay uses (the
// platform creates them; the relay never does).
func kafka(t *testing.T) (*kfake.Cluster, *kgo.Client) {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(6, relay.Topics()...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	opts, err := relay.Config{Brokers: c.ListenAddrs()}.Options()
	if err != nil {
		t.Fatal(err)
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	return c, cl
}

// consume reads every committed record from the relay's topics.
func consume(t *testing.T, c *kfake.Cluster, want int) []*kgo.Record {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...), kgo.ConsumeTopics(relay.Topics()...),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []*kgo.Record
	for len(out) < want && ctx.Err() == nil {
		f := cl.PollFetches(ctx)
		f.EachRecord(func(r *kgo.Record) { out = append(out, r) })
	}
	return out
}

func newRelay(pool *pgxpool.Pool, p relay.Producer, reg *metrics.Registry) *relay.Relay {
	return &relay.Relay{Pool: pool, Producer: p, Content: &contract.Mapper{Options: conformance.Options{Production: true}},
		Log: quiet, Metrics: reg, Batch: 50}
}

func drain(t *testing.T, r *relay.Relay) (sent, messages, refused int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		res, err := r.Once(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		sent, messages, refused = sent+res.Sent, messages+res.Messages, refused+res.Refused
		if res.Sent == 0 && res.Refused == 0 {
			return
		}
	}
	t.Fatal("the outbox never emptied")
	return
}

func header(r *kgo.Record, k string) string {
	for _, h := range r.Headers {
		if h.Key == k {
			return string(h.Value)
		}
	}
	return ""
}

// A real run's outbox goes to Kafka: every event sent once, in committed
// transactions, keyed and typed as the contract says.
func TestRelaySendsTheOutbox(t *testing.T) {
	run := collecttest.Run(t)
	ctx := context.Background()
	// A discovery candidate, and the coverage roll-ups of the run's hour.
	if _, err := run.Pool.Exec(ctx, `INSERT INTO candidates (channel_id, sources) VALUES ('UCfakeRELAYCAND000000001', ARRAY['web_search'])`); err != nil {
		t.Fatal(err)
	}
	ev := outbox.New(ctx, time.Now())
	if _, err := ev.Content(ctx, run.Pool, outbox.Candidates, "UCfakeRELAYCAND000000001"); err != nil {
		t.Fatal(err)
	}
	if n, err := ev.Coverage(ctx, run.Pool, time.Now().Add(-2*time.Hour), time.Now().Add(2*time.Hour)); err != nil || n == 0 {
		t.Fatalf("coverage roll-ups: %d (%v)", n, err)
	}
	c, cl := kafka(t)
	reg := metrics.New()
	r := newRelay(run.Pool, cl, reg)
	r.Content.Keywords = func() contract.KeywordIDs { return run.Live.Matcher() }
	var total int
	if err := run.Pool.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	sent, messages, refused := drain(t, r)
	if sent != total || refused != 0 {
		t.Fatalf("sent %d of %d events, %d refused", sent, total, refused)
	}
	var unsent int
	_ = run.Pool.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox WHERE published_at IS NULL`).Scan(&unsent)
	if unsent != 0 {
		t.Errorf("%d events still unsent", unsent)
	}
	recs := consume(t, c, messages)
	if len(recs) != messages {
		t.Fatalf("read %d committed records, want %d", len(recs), messages)
	}
	byTopic := map[string]int{}
	for _, rec := range recs {
		byTopic[rec.Topic]++
		typ := header(rec, "sb-type")
		if typ == "" {
			t.Errorf("%s record without sb-type", rec.Topic)
		}
		// Every contract message (content.*, discovery.*, collection.*) says
		// its contract version; yts's own yt.* don't.
		if !strings.HasPrefix(rec.Topic, "yt.") != (header(rec, "sb-contract") == "1.2.0") {
			t.Errorf("%s record: sb-contract %q", rec.Topic, header(rec, "sb-contract"))
		}
		if (rec.Topic == contract.TopicComment || rec.Topic == contract.TopicMetrics || rec.Topic == "yt.comment.v1") &&
			!strings.HasPrefix(string(rec.Key), "yt:post:") {
			t.Errorf("%s keyed %q, want its post", rec.Topic, rec.Key)
		}
	}
	for _, tp := range relay.Topics() {
		if byTopic[tp] == 0 {
			t.Errorf("nothing on %s (by topic: %v)", tp, byTopic)
		}
	}
	// What arrives decodes as what was sent.
	for _, rec := range recs {
		switch rec.Topic {
		case contract.TopicPost:
			var p contentv1.Post
			if err := proto.Unmarshal(rec.Value, &p); err != nil || p.GetEnvelope().GetId() != string(rec.Key) {
				t.Errorf("content.post %q: %v (%v)", rec.Key, p.GetEnvelope(), err)
			}
		case "yt.video.v1":
			var v ytv1.Video
			if err := proto.Unmarshal(rec.Value, &v); err != nil || "yt:post:"+v.GetId() != string(rec.Key) || v.GetEvent().GetEventId() == "" {
				t.Errorf("yt.video %q: %v", rec.Key, err)
			}
		}
	}
	var b strings.Builder
	_, _ = reg.WriteTo(&b)
	if !strings.Contains(b.String(), `yt_relay_transactions_total{result="committed"}`) || !strings.Contains(b.String(), "yt_relay_published_total") {
		t.Errorf("metrics:\n%s", b.String())
	}
}

// An event the contract refuses, and one Kafka can't take, are set aside
// with the reason; the others go; a restart tries them again.
func TestRefusedAndOversizedEvents(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, sql := range []string{
		`INSERT INTO channels (id, kind, tier, status) VALUES ('UCfakeRELAY0000000000001', 'core', 'B', 'listed')`,
		`INSERT INTO videos (id, channel_id, kind, title, live_status, published_at, published_precision_s)
			VALUES ('fakeRLv1', 'UCfakeRELAY0000000000001', 'video', 'fine', 'none', '2026-11-07 08:00:00+00', 0)`,
		`INSERT INTO videos (id, channel_id, kind, title, description, live_status, published_at, published_precision_s)
			VALUES ('fakeRLv2', 'UCfakeRELAY0000000000001', 'video', 'huge', repeat('x', 1500000), 'none', '2026-11-07 08:00:00+00', 0)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	ev := outbox.New(ctx, at)
	for _, w := range []struct {
		tb outbox.Table
		id string
	}{{outbox.Channels, "UCfakeRELAY0000000000001"}, {outbox.Videos, "fakeRLv1"}, {outbox.Videos, "fakeRLv2"}} {
		if _, err := ev.Content(ctx, pool, w.tb, w.id); err != nil {
			t.Fatal(err)
		}
	}
	c, cl := kafka(t)
	r := newRelay(pool, cl, nil)
	// The nameless channel is refused at once; the oversized video aborts
	// its batch, is set aside, and the batch goes again without it.
	res, err := r.Once(ctx)
	if err == nil || res.Refused != 2 {
		t.Fatalf("first batch: %+v, err %v; want the refusal and the oversized one set aside, and an abort", res, err)
	}
	sent, _, _ := drain(t, r)
	if sent != 1 {
		t.Fatalf("sent %d, want the fine video", sent)
	}
	failures := map[string]string{}
	rows, _ := pool.Query(ctx, `SELECT entity_id, failure FROM event_outbox WHERE published_at IS NULL`)
	for rows.Next() {
		var id, f string
		_ = rows.Scan(&id, &f)
		failures[id] = f
	}
	rows.Close()
	if !strings.Contains(failures["UCfakeRELAY0000000000001"], "neither display_name nor handle") ||
		!strings.Contains(failures["fakeRLv2"], "kafka:") || len(failures) != 2 {
		t.Errorf("failures %v", failures)
	}
	// Only the fine video's two messages were committed; the aborted
	// batch is invisible to readers.
	if recs := consume(t, c, 2); len(recs) != 2 {
		t.Errorf("read %d committed records, want 2", len(recs))
	}
	if n, err := outbox.RetryFailed(ctx, pool); err != nil || n != 2 {
		t.Errorf("retry: %d (%v), want 2", n, err)
	}
}

// Kafka settings: the brokers are required, credentials go together, and
// never in the clear to another machine.
func TestLoadConfig(t *testing.T) {
	write := func(dir string, files map[string]string) {
		for k, v := range files {
			if err := writeFile(dir+"/"+k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	dir := t.TempDir()
	if _, err := relay.LoadConfig(dir); err == nil {
		t.Error("no brokers must be an error")
	}
	write(dir, map[string]string{"KAFKA_BROKERS": "kafka-1.internal:9093, kafka-2.internal:9093", "KAFKA_USER": "yts", "KAFKA_PASSWORD": "pw"})
	if _, err := relay.LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "without TLS") {
		t.Errorf("credentials in the clear: %v", err)
	}
	write(dir, map[string]string{"KAFKA_TLS": "true", "KAFKA_SASL_MECHANISM": "SCRAM-SHA-256"})
	c, err := relay.LoadConfig(dir)
	if err != nil || len(c.Brokers) != 2 || !c.TLS || c.User != "yts" {
		t.Fatalf("config %+v, err %v", c, err)
	}
	if _, err := c.Options(); err != nil {
		t.Error(err)
	}
	c.Mechanism = "GSSAPI"
	if _, err := c.Options(); err == nil {
		t.Error("an unknown mechanism must be an error")
	}
}
