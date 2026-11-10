package handoff_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/handoff"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/relay"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const (
	ch   = "UCfakeHANDOFF00000000001"
	shaA = "aaaaaaaaaaaaaaaa00000000000000000000000000000000000000000000000a"
	shaB = "bbbbbbbbbbbbbbbb00000000000000000000000000000000000000000000000b"
	shaC = "cccccccccccccccc00000000000000000000000000000000000000000000000c"
)

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seed stores two thumbnails (A and B) and the channel's avatar, files and
// rows, as the media jobs do.
func seed(t *testing.T, pool *pgxpool.Pool, store media.Store, at time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, m := range []struct{ kind, entity, sha string }{{"thumb", "fakeHOv1", shaA}, {"thumb", "fakeHOv2", shaB}, {"avatar", ch, shaC}} {
		key := "media/" + m.kind + "/" + m.entity + "/" + m.sha[:16] + ".jpg"
		if err := store.Put(ctx, key, "image/jpeg", []byte("img")); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media (kind, entity_id, channel_id, sha256, object_key, source_url, content_type, bytes, first_seen, last_seen)
			VALUES ($1, $2, $3, $4, $5, 'https://i.ytimg.com/x', 'image/jpeg', 3, $6, $6)`, m.kind, m.entity, ch, m.sha, key, at); err != nil {
			t.Fatal(err)
		}
	}
}

func ack(id, consumer string, st mediav1.ProcessStatus) *mediav1.MediaProcessed {
	return &mediav1.MediaProcessed{EventId: "e-" + id, MediaId: id, Consumer: consumer, Status: st, Platform: "yt",
		ProcessedAt: timestamppb.New(time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)), Reason: "x"}
}

const (
	idA = "yt:media:thumb:fakeHOv1:aaaaaaaaaaaaaaaa"
	idB = "yt:media:thumb:fakeHOv2:bbbbbbbbbbbbbbbb"
)

// Thumbnails are announced once with their deadline; a kind not listed
// (here the avatar) never is. An acknowledged file is deleted; a retried one
// is announced again, at most 4 times in all, and deleted at its deadline.
// The rows stay.
func TestHandoff(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	store := media.FileStore{Dir: t.TempDir()}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	seed(t, pool, store, now.Add(-time.Hour))
	reg := metrics.New()
	h := &handoff.Handoff{Pool: pool, Store: store, Kinds: []string{media.Thumb}, Consumers: []string{"sb-analysis"},
		Log: quiet, Metrics: reg, Now: func() time.Time { return now }}

	if n, err := h.Announce(ctx, 100); err != nil || n != 2 {
		t.Fatalf("announced %d (%v), want the two thumbnails", n, err)
	}
	if n, _ := h.Announce(ctx, 100); n != 0 {
		t.Errorf("announced again: %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE kind = 'thumb' AND attempt = 1 AND delete_after = $1`, now.Add(handoff.DefaultKeep)); n != 2 {
		t.Errorf("%d thumbnails with their 72-hour deadline", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE kind = 'avatar' AND announced_at IS NULL`); n != 1 {
		t.Error("a kind not listed is not handed off")
	}
	if n := count(t, pool, `SELECT count(*) FROM event_outbox WHERE kind = 'media_ready'`); n != 2 {
		t.Errorf("%d media_ready events", n)
	}

	// What isn't ours, or isn't a required consumer's, is ignored.
	for _, m := range []*mediav1.MediaProcessed{
		{MediaId: idA, Consumer: "sb-analysis", Status: mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED, Platform: "fb"},
		ack(idA, "someone-else", mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED),
		ack("yt:post:fakeHOv1", "sb-analysis", mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED),
		ack("yt:media:thumb:fakeGONE:aaaaaaaaaaaaaaaa", "sb-analysis", mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED),
	} {
		if err := h.Process(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM media_acks`); n != 0 {
		t.Fatalf("%d acknowledgements recorded from what isn't ours", n)
	}

	// A is processed; B fails, retryably, five times.
	if err := h.Process(ctx, ack(idA, "sb-analysis", mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := h.Process(ctx, ack(idB, "sb-analysis", mediav1.ProcessStatus_PROCESS_STATUS_FAILED_RETRYABLE)); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, pool, `SELECT attempt FROM media WHERE entity_id = 'fakeHOv2'`); n != handoff.MaxAttempt {
		t.Errorf("B announced %d times, want %d", n, handoff.MaxAttempt)
	}
	if n := count(t, pool, `SELECT count(*) FROM event_outbox WHERE kind = 'media_ready' AND entity_id LIKE 'thumb:fakeHOv2:%'`); n != handoff.MaxAttempt {
		t.Errorf("%d announcements of B", n)
	}

	// The deletion round: A goes (processed); B waits for its deadline.
	if n, err := h.DeleteDue(ctx, 100); err != nil || n != 1 {
		t.Fatalf("deleted %d (%v), want A", n, err)
	}
	if _, err := store.Get(ctx, "media/thumb/fakeHOv1/aaaaaaaaaaaaaaaa.jpg"); !errors.Is(err, media.ErrNotFound) {
		t.Errorf("A's file: %v", err)
	}
	if _, err := store.Get(ctx, "media/thumb/fakeHOv2/bbbbbbbbbbbbbbbb.jpg"); err != nil {
		t.Errorf("B's file must wait for its deadline: %v", err)
	}
	now = now.Add(handoff.DefaultKeep)
	if n, err := h.DeleteDue(ctx, 100); err != nil || n != 1 {
		t.Fatalf("at the deadline: deleted %d (%v), want B", n, err)
	}
	if n, _ := h.DeleteDue(ctx, 100); n != 0 {
		t.Errorf("deleted again: %d", n)
	}
	for entity, reason := range map[string]string{"fakeHOv1": "processed", "fakeHOv2": "expired"} {
		if n := count(t, pool, `SELECT count(*) FROM media WHERE entity_id = $1 AND deleted_at IS NOT NULL AND delete_reason = $2`, entity, reason); n != 1 {
			t.Errorf("%s: not recorded as deleted (%s)", entity, reason)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM event_outbox WHERE kind = 'media_deleted'`); n != 2 {
		t.Errorf("%d media_deleted events", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM media`); n != 3 {
		t.Error("the rows stay after the files go")
	}
	// A retry after the file went announces nothing.
	if err := h.Process(ctx, ack(idA, "sb-analysis", mediav1.ProcessStatus_PROCESS_STATUS_FAILED_RETRYABLE)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM event_outbox WHERE kind = 'media_ready' AND entity_id LIKE 'thumb:fakeHOv1:%'`); n != 1 {
		t.Errorf("A announced after its deletion: %d", n)
	}
	var b strings.Builder
	_, _ = reg.WriteTo(&b)
	for _, want := range []string{"yt_media_announced_total 2", `yt_media_deleted_total{reason="processed"} 1`,
		`yt_media_deleted_total{reason="expired"} 1`, `yt_media_acks_total{consumer="sb-analysis",status="failed_retryable"} 6`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("metrics miss %q:\n%s", want, b.String())
		}
	}
}

// failingStore can't delete.
type failingStore struct{ media.Store }

func (failingStore) Delete(context.Context, string) error { return errors.New("storage unavailable") }

// A file storage won't delete stays, with its row unchanged, and is tried
// again next round.
func TestDeleteFailureIsRetried(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	store := media.FileStore{Dir: t.TempDir()}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	seed(t, pool, store, now)
	h := &handoff.Handoff{Pool: pool, Store: failingStore{store}, Kinds: media.HandoffKinds, Consumers: []string{"sb-analysis"},
		Log: quiet, Now: func() time.Time { return now }}
	if _, err := h.Announce(ctx, 100); err != nil {
		t.Fatal(err)
	}
	now = now.Add(handoff.DefaultKeep)
	if n, err := h.DeleteDue(ctx, 100); err != nil || n != 0 {
		t.Fatalf("deleted %d (%v) with storage down", n, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE deleted_at IS NOT NULL`); n != 0 {
		t.Error("a file not deleted must not be recorded as deleted")
	}
	h.Store = store
	if n, err := h.DeleteDue(ctx, 100); err != nil || n != 3 {
		t.Errorf("storage back: deleted %d (%v), want the two thumbnails and the avatar", n, err)
	}
}

// Acknowledgements arrive through Kafka: read in group yts-media, recorded,
// then committed, so a restart doesn't read them again.
func TestConsume(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	store := media.FileStore{Dir: t.TempDir()}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	seed(t, pool, store, now)
	h := &handoff.Handoff{Pool: pool, Store: store, Kinds: media.HandoffKinds, Consumers: []string{"sb-analysis"},
		Log: quiet, Now: func() time.Time { return now }}
	if _, err := h.Announce(ctx, 100); err != nil {
		t.Fatal(err)
	}

	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, handoff.TopicProcessed))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	prod, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	for _, m := range []*mediav1.MediaProcessed{ack(idA, "sb-analysis", mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED),
		ack(idB, "sb-analysis", mediav1.ProcessStatus_PROCESS_STATUS_SKIPPED)} {
		v, _ := proto.Marshal(m)
		if err := prod.ProduceSync(ctx, &kgo.Record{Topic: handoff.TopicProcessed, Key: []byte(m.GetMediaId()), Value: v}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	if err := prod.ProduceSync(ctx, &kgo.Record{Topic: handoff.TopicProcessed, Value: []byte("not a message")}).FirstErr(); err != nil {
		t.Fatal(err)
	}

	opts, err := relay.Config{Brokers: c.ListenAddrs()}.ConsumerOptions("yts-media", handoff.TopicProcessed)
	if err != nil {
		t.Fatal(err)
	}
	consume := func(wait time.Duration) {
		t.Helper()
		cl, err := kgo.NewClient(opts...)
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		cctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- h.Consume(cctx, cl) }()
		for cctx.Err() == nil && count(t, pool, `SELECT count(*) FROM media_acks`) < 2 {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond) // let the batch's offsets be committed
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	consume(10 * time.Second)
	if n := count(t, pool, `SELECT count(*) FROM media_acks WHERE consumer = 'sb-analysis' AND status IN ('processed', 'skipped')`); n != 2 {
		t.Fatalf("%d acknowledgements recorded, want 2", n)
	}
	// Both are terminal: both files go now, before their deadline.
	if n, err := h.DeleteDue(ctx, 100); err != nil || n != 2 {
		t.Errorf("deleted %d (%v), want 2", n, err)
	}

	// A restart in the same group reads nothing again.
	if _, err := pool.Exec(ctx, `DELETE FROM media_acks`); err != nil {
		t.Fatal(err)
	}
	consume(2 * time.Second)
	if n := count(t, pool, `SELECT count(*) FROM media_acks`); n != 0 {
		t.Errorf("%d acknowledgements read again after a restart", n)
	}
}

// By default (the owner, 11 November) channel avatars and banners are handed
// off with the thumbnails; commenters' avatars only when listed.
func TestHandoffKinds(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	store := media.FileStore{Dir: t.TempDir()}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	seed(t, pool, store, now.Add(-time.Hour))
	h := &handoff.Handoff{Pool: pool, Store: store, Kinds: media.HandoffKinds, Consumers: []string{"sb-analysis"},
		Log: quiet, Now: func() time.Time { return now }}
	if n, err := h.Announce(ctx, 100); err != nil || n != 3 {
		t.Fatalf("announced %d (%v), want the two thumbnails and the avatar", n, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE kind = 'avatar' AND attempt = 1 AND delete_after = $1`, now.Add(handoff.DefaultKeep)); n != 1 {
		t.Error("the channel's avatar is announced with its deadline")
	}
	for _, c := range []struct {
		in   string
		want int
		bad  bool
	}{
		{"thumb,avatar,banner", 3, false},
		{" thumb , author_avatar,thumb", 2, false},
		{"thumb,poster", 0, true},
		{",", 0, true},
	} {
		got, err := media.ParseKinds(c.in)
		if (err != nil) != c.bad || len(got) != c.want {
			t.Errorf("ParseKinds(%q) = %v, %v", c.in, got, err)
		}
	}
}
