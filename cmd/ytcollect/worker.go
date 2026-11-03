package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
)

// More secret files and folders the worker uses.
const (
	EnvProxiesFile     = "YT_PROXIES_FILE"
	DefaultProxiesFile = "/etc/securebrand-youtube/proxies.txt"
	EnvDebugDir        = "YT_DEBUG_DIR"
	DefaultDebugDir    = "/var/lib/securebrand-youtube/debug"
)

// workerCmd runs the collector: it leases jobs and reads YouTube through
// the proxies. Each worker process owns the proxies in its file; two
// processes must not share an IP (their paces would add up).
func workerCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	interval := fs.Duration("interval", 4*time.Second, "minimum gap between requests on one proxy IP (Phase 0 E1 measures the safe value)")
	workers := fs.Int("workers", 10, "jobs at the same time")
	noPlayer := fs.Bool("no-player", false, "skip the player endpoint (tags, category)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lines, err := secrets.ReadLines(pathFromEnv(EnvProxiesFile, DefaultProxiesFile))
	if err != nil {
		return fmt.Errorf("proxy list: %w", err)
	}
	proxies, err := proxypool.ParseList(lines)
	if err != nil {
		return err
	}
	keyer, err := privacy.LoadKeyer(pathFromEnv(EnvPrivacyKeyFile, DefaultPrivacyKeyFile))
	if err != nil {
		return fmt.Errorf("privacy key: %w", err)
	}
	sup := privacy.NewSuppressor(keyer)
	if err := sup.Refresh(ctx, pool); err != nil {
		return fmt.Errorf("suppression list: %w", err)
	}
	live := &kwstore.Live{}
	if _, err := live.Refresh(ctx, pool); err != nil {
		return fmt.Errorf("keywords: %w", err)
	}
	debug, err := phase0.OpenStore(pathFromEnv(EnvDebugDir, DefaultDebugDir))
	if err != nil {
		return err
	}
	pp := proxypool.New(proxies, proxypool.Config{MinInterval: *interval}, time.Now().UnixNano())
	fetch := &phase0.Collector{Client: innertube.New(innertube.Options{Pool: pp}), Store: debug, Now: time.Now, Log: log}
	svc := &collect.Service{Pool: pool, Fetch: fetch, Sup: sup, Log: log, WithPlayer: !*noPlayer,
		Write: &writer.Writer{Pool: pool, Sup: sup, KW: live}}

	// Keep the suppression list and the keywords current.
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := sup.Refresh(ctx, pool); err != nil && ctx.Err() == nil {
					log.Warn("refreshing the suppression list failed", "err", err)
				}
			}
		}
	}()
	go live.Run(ctx, pool, time.Minute, func(err error) { log.Warn("refreshing keywords failed", "err", err) })

	host, _ := os.Hostname()
	w := &worker.Worker{Pool: pool, Owner: fmt.Sprintf("%s/%d", host, os.Getpid()), Handlers: svc.Handlers(),
		Concurrency: *workers, Lease: 15 * time.Minute, Log: log}
	log.Info("worker running", "proxies", len(proxies), "interval", interval.String(), "workers", *workers)
	return w.Run(ctx)
}
