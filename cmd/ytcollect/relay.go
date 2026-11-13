package main

import (
	"context"
	"errors"
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
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/handoff"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ops"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/relay"
)

// EnvKafkaDir names the folder with the platform's Kafka files (KAFKA_*);
// without it, the platform's credentials folder (YT_POSTGRES_DIR).
const EnvKafkaDir = "YT_KAFKA_DIR"

// relayCmd publishes the outbox to Kafka: the content.* messages, checked
// against the contract, and the yt.* messages (REQ-010 fix 12). With
// object storage it also runs the media hand-off: stored images are
// announced to analysis and deleted once acknowledged or at their deadline
// (internal/handoff). A long-running service; one sends at a time.
func relayCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	batch := fs.Int("batch", 500, "events per Kafka transaction")
	idle := fs.Duration("idle", 30*time.Second, "the longest wait when nothing is waiting to be sent (waits start at 1s and double)")
	canary := fs.Bool("canary", false, "check messages as the canary does (the contract allows test-only values; owner D-012)")
	metricsAddr := fs.String("metrics-addr", ":9103", "the ops port: /metrics, /healthz and /readyz (empty: off)")
	transactions := fs.Bool("transactions", true, "send each batch in one Kafka transaction (false: idempotent sends without transactions, for a Kafka that can't grant the transactional ID)")
	handOff := fs.Bool("media-handoff", true, "hand stored images to analysis and delete them once analysed (needs object storage)")
	mediaKeep := fs.Duration("media-keep", handoff.DefaultKeep, "an announced image's deadline: deleted then even without every acknowledgement (at least 24h)")
	mediaGroup := fs.String("media-group", "yts-media", "the Kafka consumer group that reads the acknowledgements")
	mediaConsumers := fs.String("media-consumers", strings.Join(contract.DefaultConsumers, ","), "the consumers that must acknowledge each image, comma-separated")
	mediaKinds := fs.String("media-kinds", strings.Join(media.HandoffKinds, ","), "the images handed off and deleted once analysed, comma-separated: thumb, avatar, banner, post_image, author_avatar")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *batch < 1 || *batch > 5000 {
		return fmt.Errorf("-batch must be between 1 and 5000, not %d", *batch)
	}
	if *mediaKeep < handoff.MinKeep {
		return fmt.Errorf("-media-keep must be at least %s (the contract), not %s", handoff.MinKeep, *mediaKeep)
	}
	kinds, err := media.ParseKinds(*mediaKinds)
	if err != nil {
		return err
	}
	var consumers []string
	for _, c := range strings.Split(*mediaConsumers, ",") {
		if c = strings.TrimSpace(c); c != "" {
			consumers = append(consumers, c)
		}
	}
	if *handOff && len(consumers) == 0 {
		return errors.New("-media-consumers is empty")
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
	opts, err := cfg.ProducerOptions(*transactions)
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

	mapper := &contract.Mapper{Options: conformance.Options{Production: true, Canary: *canary},
		Keywords: func() contract.KeywordIDs { return live.Matcher() }, MediaConsumers: consumers}
	r := &relay.Relay{Pool: pool, Producer: client, Batch: *batch, Idle: *idle, Log: log, Metrics: reg, Content: mapper,
		NoTransactions: !*transactions}
	var store media.Store
	if *handOff {
		if store, err = openStorage(log); err != nil {
			return err
		}
		if store == nil {
			log.Info("no object storage: images are not handed to analysis")
		}
	}
	if store != nil {
		mapper.MediaBucket, mapper.MediaPrefix = media.Location(store)
		h := &handoff.Handoff{Pool: pool, Store: store, Kinds: kinds, Consumers: consumers, Keep: *mediaKeep,
			Log: log, Metrics: reg}
		copts, err := cfg.ConsumerOptions(*mediaGroup, handoff.TopicProcessed)
		if err != nil {
			return err
		}
		// Only the relay holding the lease hands images off.
		r.Lead = func(ctx context.Context) { h.Run(ctx, func() (*kgo.Client, error) { return kgo.NewClient(copts...) }) }
		log.Info("media hand-off on", "kinds", kinds, "consumers", consumers, "keep", *mediaKeep, "group", *mediaGroup)
	}

	log.Info("relay starting", "brokers", len(cfg.Brokers), "tls", cfg.TLS, "batch", *batch, "canary", *canary, "transactions", *transactions)
	return r.Run(ctx)
}
