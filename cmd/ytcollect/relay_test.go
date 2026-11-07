package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/relay"
)

func TestRelayCommand(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv(EnvKafkaDir, "")
	t.Setenv(EnvPostgresDir, "")
	if err := relayCmd(ctx, pool, quiet, nil); err == nil || !strings.Contains(err.Error(), "KAFKA_") {
		t.Errorf("no Kafka settings: %v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, title, live_status, published_at, published_precision_s)
		VALUES ('fakeRCv1', 'UCfakeRELAYCMD0000000001', 'video', 'A video', 'none', '2026-11-07 08:00:00+00', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := outbox.New(ctx, time.Now()).Write(ctx, pool, outbox.Videos, "fakeRCv1"); err != nil {
		t.Fatal(err)
	}
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, relay.Topics()...))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "KAFKA_BROKERS"), []byte(strings.Join(c.ListenAddrs(), ",")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKafkaDir, dir)
	if err := relayCmd(ctx, pool, quiet, []string{"-batch", "0"}); err == nil {
		t.Error("-batch 0 must be refused")
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- relayCmd(rctx, pool, quiet, []string{"-metrics-addr", "", "-idle", "50ms"}) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var unsent int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE published_at IS NULL`).Scan(&unsent)
		if unsent == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var sent int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE published_at IS NOT NULL`).Scan(&sent)
	if sent != 2 {
		t.Errorf("%d events sent, want the video and its counts", sent)
	}
}
