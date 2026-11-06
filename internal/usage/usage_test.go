package usage

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

func TestMeter(t *testing.T) {
	var none *Meter
	none.Request(100) // a nil meter (no job) ignores everything
	none.Block()
	if none.Totals() != (Totals{}) || From(context.Background()) != nil {
		t.Fatal("a missing meter must count nothing")
	}
	ctx, m := With(context.Background())
	if From(ctx) != m {
		t.Fatal("From must return the context's meter")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			From(ctx).Request(50)
			From(ctx).Block()
		}()
	}
	wg.Wait()
	m.Request(-5) // a request with no known size counts, with 0 bytes
	if got := m.Totals(); got != (Totals{Requests: 11, Bytes: 500, Blocks: 10}) {
		t.Errorf("totals = %+v", got)
	}
}

func TestDayIsIraqTime(t *testing.T) {
	// 21:30 UTC on 9 Nov is 00:30 on 10 Nov in Iraq.
	if d := Day(time.Date(2026, 11, 9, 21, 30, 0, 0, time.UTC)); d != "2026-11-10" {
		t.Errorf("Day = %s; want 2026-11-10", d)
	}
	if d := Day(time.Date(2026, 11, 9, 20, 59, 0, 0, time.UTC)); d != "2026-11-09" {
		t.Errorf("Day = %s; want 2026-11-09", d)
	}
}

func TestPerChannelAndGroup(t *testing.T) {
	for kind, want := range map[string]bool{
		queue.KindRSS: true, queue.KindComments: true, queue.KindBackfill: true, queue.KindVideo: true,
		queue.KindCandidate: false, queue.KindSearch: false, queue.KindEnrich: false, queue.KindMedia: false, queue.KindResolve: false,
	} {
		if PerChannel(kind) != want {
			t.Errorf("PerChannel(%s) = %v", kind, !want)
		}
	}
	for kind, want := range map[string]string{
		queue.KindComments: GroupChannel, queue.KindBackfill: GroupBackfill, queue.KindEnrich: GroupFollows,
		queue.KindMedia: GroupFollows, queue.KindCandidate: GroupDiscovery, queue.KindSearch: GroupDiscovery,
	} {
		if Group(kind) != want {
			t.Errorf("Group(%s) = %s; want %s", kind, Group(kind), want)
		}
	}
}

func TestRecord(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 9, 21, 10, 0, 0, time.UTC) // 00:10 on 10 Nov, Iraq time
	ch := "UCfakeAAAAAAAAAAAAAAAAAA"
	rec := func(kind, channel string, tot Totals, failed bool) {
		t.Helper()
		j := queue.Job{Kind: kind, EntityID: "fake-" + kind, ChannelID: channel}
		if err := Record(ctx, pool, at, j, tot, failed, 1500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	rec(queue.KindComments, ch, Totals{Requests: 4, Bytes: 4000, Blocks: 1}, false)
	rec(queue.KindComments, ch, Totals{Requests: 2, Bytes: 1000}, true)
	rec(queue.KindBackfill, ch, Totals{Requests: 30, Bytes: 90000}, false)
	// A candidate check names a channel that isn't monitored: hourly only.
	rec(queue.KindCandidate, "UCfakeCANDIDATEAAAAAAAAA", Totals{Requests: 3, Bytes: 300}, false)
	rec(queue.KindSearch, "", Totals{Requests: 4, Bytes: 400}, false)

	var jobs, fails, reqs, bytes, blocks, ms int64
	if err := pool.QueryRow(ctx, `SELECT jobs, failures, requests, bytes, blocks, run_ms FROM usage_hourly
		WHERE hour = '2026-11-09 21:00:00+00' AND kind = 'comments'`).Scan(&jobs, &fails, &reqs, &bytes, &blocks, &ms); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || fails != 1 || reqs != 6 || bytes != 5000 || blocks != 1 || ms != 3000 {
		t.Errorf("hourly comments = %d jobs, %d failed, %d requests, %d bytes, %d blocks, %d ms", jobs, fails, reqs, bytes, blocks, ms)
	}
	var day string
	var bfReq, bfBytes int64
	if err := pool.QueryRow(ctx, `SELECT day::text, jobs, requests, bytes, backfill_requests, backfill_bytes FROM usage_channel_daily
		WHERE channel_id = $1`, ch).Scan(&day, &jobs, &reqs, &bytes, &bfReq, &bfBytes); err != nil {
		t.Fatal(err)
	}
	if day != "2026-11-10" || jobs != 3 || reqs != 36 || bytes != 95000 || bfReq != 30 || bfBytes != 90000 {
		t.Errorf("channel day = %s: %d jobs, %d requests, %d bytes, backfill %d / %d", day, jobs, reqs, bytes, bfReq, bfBytes)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usage_channel_daily`).Scan(&n); err != nil || n != 1 {
		t.Errorf("channel rows = %d (%v); want only the monitored channel", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usage_hourly`).Scan(&n); err != nil || n != 4 {
		t.Errorf("hourly rows = %d (%v); want comments, backfill, candidate, search", n, err)
	}
}

func TestSampleSize(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Now()
	if err := db.EnsurePartitions(ctx, pool, now, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO video_snapshots (video_id, taken_at, views) VALUES ('fakevid0001', now(), 1)`); err != nil {
		t.Fatal(err)
	}
	if err := SampleSize(ctx, pool, now); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	var tables map[string]int64
	if err := pool.QueryRow(ctx, `SELECT bytes, tables FROM db_sizes`).Scan(&bytes, &tables); err != nil {
		t.Fatal(err)
	}
	if bytes <= 0 || len(tables) == 0 {
		t.Fatalf("size %d, tables %v", bytes, tables)
	}
	// A partitioned table is counted under its own name, not its partitions.
	if tables["video_snapshots"] <= 0 {
		t.Errorf("tables = %v; want video_snapshots (partitioned) counted", tables)
	}
	for name := range tables {
		if strings.HasPrefix(name, "video_snapshots_") {
			t.Errorf("partition %s listed on its own", name)
		}
	}
	// Running twice in the same second is harmless.
	if err := SampleSize(ctx, pool, now); err != nil {
		t.Fatal(err)
	}
}
