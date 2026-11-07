package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/abdullah95k/securebrand-contracts/conformance"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/contract"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ops"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/relay"
)

// EnvKafkaDir names the folder with the platform's Kafka files (KAFKA_*);
// without it, the platform's credentials folder (YT_POSTGRES_DIR).
const EnvKafkaDir = "YT_KAFKA_DIR"

// relayCmd publishes the outbox to Kafka: the content.* messages, checked
// against the contract, and the yt.* messages (REQ-010 fix 12). A
// long-running service; one sends at a time.
func relayCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	batch := fs.Int("batch", 500, "events per Kafka transaction")
	idle := fs.Duration("idle", 2*time.Second, "wait when nothing is waiting to be sent")
	canary := fs.Bool("canary", false, "check messages as the canary does (the contract allows test-only values; owner D-012)")
	metricsAddr := fs.String("metrics-addr", ":9103", "the ops port: /metrics, /healthz and /readyz (empty: off)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *batch < 1 || *batch > 5000 {
		return fmt.Errorf("-batch must be between 1 and 5000, not %d", *batch)
	}
	dir := strings.TrimSpace(os.Getenv(EnvKafkaDir))
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv(EnvPostgresDir))
	}
	if dir == "" {
		return fmt.Errorf("relay: set %s (or %s) to the folder with the KAFKA_* files", EnvKafkaDir, EnvPostgresDir)
	}
	cfg, err := relay.LoadConfig(dir)
	if err != nil {
		return err
	}
	opts, err := cfg.Options()
	if err != nil {
		return err
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("relay: Kafka client: %w", err)
	}
	defer client.Close()
	live := &kwstore.Live{}
	if _, err := live.Refresh(ctx, pool); err != nil {
		return fmt.Errorf("keywords: %w", err)
	}
	go live.Run(ctx, pool, time.Minute, func(err error) { log.Warn("refreshing keywords failed", "err", err) })

	reg := metrics.New()
	health := &ops.Health{}
	health.Add("database", pool.Ping)
	health.Add("kafka", func(ctx context.Context) error { return client.Ping(ctx) })
	serveOps(ctx, *metricsAddr, reg, health, log)

	r := &relay.Relay{Pool: pool, Producer: client, Batch: *batch, Idle: *idle, Log: log, Metrics: reg,
		Content: &contract.Mapper{Options: conformance.Options{Production: true, Canary: *canary},
			Keywords: func() contract.KeywordIDs { return live.Matcher() }}}
	log.Info("relay starting", "brokers", len(cfg.Brokers), "tls", cfg.TLS, "batch", *batch, "canary", *canary)
	return r.Run(ctx)
}
