package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/capacity"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/comments"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/detect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/discovery"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/enrich"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/erasure"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/feeds"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fill"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/monitor"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/websub"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
)

// More secret files and folders the worker uses.
const (
	EnvProxiesFile     = "YT_PROXIES_FILE"
	DefaultProxiesFile = "/etc/securebrand-youtube/proxies.txt"
	EnvDebugDir        = "YT_DEBUG_DIR"
	DefaultDebugDir    = "/var/lib/securebrand-youtube/debug"
	EnvAPIKeyFile      = "YT_API_KEY_FILE"
	DefaultAPIKeyFile  = "/etc/securebrand-youtube/youtube_api_key"
	// Push notifications (off until a public subdomain exists, D23).
	EnvWebSubCallback       = "YT_WEBSUB_CALLBACK" // e.g. https://push.example.com/websub
	EnvWebSubSecretFile     = "YT_WEBSUB_SECRET_FILE"
	DefaultWebSubSecretFile = "/etc/securebrand-youtube/websub_secret"
)

// webSubConfig reads the push settings; Enabled() is false when unset.
func webSubConfig() (websub.Config, error) {
	cb := strings.TrimSpace(os.Getenv(EnvWebSubCallback))
	if cb == "" {
		return websub.Config{}, nil
	}
	if !strings.HasPrefix(cb, "https://") {
		return websub.Config{}, fmt.Errorf("%s must be an https:// URL", EnvWebSubCallback)
	}
	secret, err := secrets.ReadFile(pathFromEnv(EnvWebSubSecretFile, DefaultWebSubSecretFile))
	if err != nil {
		return websub.Config{}, fmt.Errorf("websub secret: %w", err)
	}
	if len(secret) < 32 {
		return websub.Config{}, errors.New("websub secret: at least 32 characters (openssl rand -hex 32)")
	}
	return websub.Config{Callback: cb, Secret: []byte(secret)}, nil
}

// workerCmd runs the collector: it leases jobs and reads YouTube through
// the proxies. Each worker process owns the proxies in its file; two
// processes must not share an IP (their paces would add up).
func workerCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	interval := fs.Duration("interval", 4*time.Second, "minimum gap between requests on one proxy IP (Phase 0 E1 measures the safe value)")
	workers := fs.Int("workers", 10, "jobs at the same time")
	noPlayer := fs.Bool("no-player", false, "skip the player endpoint (tags, category)")
	commentCap := fs.Int("comment-cap", comments.DefaultCap, "most comments the first read of a video takes; capped videos are flagged partial")
	metricsAddr := fs.String("metrics-addr", ":9101", "where Prometheus reads this worker's metrics (empty: off)")
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
	rec := &fill.Recorder{}
	reg := metrics.New()
	store, err := openStorage(log)
	if err != nil {
		return err
	}
	wr := &writer.Writer{Pool: pool, Sup: sup, KW: live, Fill: rec, Media: store != nil}
	stretch := &capacity.Stretcher{Q: pool}
	svc := &collect.Service{Pool: pool, Fetch: fetch, Sup: sup, Log: log, WithPlayer: !*noPlayer, Write: wr, Stretch: stretch.Value}
	handlers := svc.Handlers()
	rss := &detect.RSS{Pool: pool, Feeds: feeds.NewFetcher(fetch.Client), Write: wr, Log: log, Stretch: stretch.Value}
	for k, h := range rss.Handlers() {
		handlers[k] = h
	}
	eng := &comments.Engine{Pool: pool, Client: fetch.Client, Sup: sup, Log: log, Cap: *commentCap, Stretch: stretch.Value, Fill: rec}
	for k, h := range eng.Handlers() {
		handlers[k] = h
	}
	// Discovery: website search always; the official API when a key file
	// exists (the quota ceiling is kept in the database).
	var api *ytapi.Client
	if keyPath := pathFromEnv(EnvAPIKeyFile, DefaultAPIKeyFile); fileExists(keyPath) {
		key, err := secrets.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("API key: %w", err)
		}
		ledger, _ := ytapi.NewLedger("", 1<<30) // the database ledger is the real ceiling
		api = ytapi.New(key, "", ledger)
	}
	disc := &discovery.Runner{Pool: pool, Fetch: fetch, API: api, Write: wr, Sup: sup, Log: log}
	for k, h := range disc.Handlers() {
		handlers[k] = h
	}
	er := &erasure.Service{Pool: pool, Keyer: keyer, Sup: sup, Fetch: fetch, Log: log, Media: store}
	for k, h := range er.Handlers() {
		handlers[k] = h
	}
	en := &enrich.Enricher{Pool: pool, Fetch: fetch, Sup: sup, Log: log, Media: store != nil}
	for k, h := range en.Handlers() {
		handlers[k] = h
	}
	if store != nil {
		ms := &media.Service{Pool: pool, Images: fetch.Client, Store: store, Sup: sup, Log: log}
		for k, h := range ms.Handlers() {
			handlers[k] = h
		}
	}
	wsCfg, err := webSubConfig()
	if err != nil {
		return err
	}
	if wsCfg.Enabled() {
		sub := &websub.Subscriber{Pool: pool, Cfg: wsCfg, Log: log}
		for k, h := range sub.Handlers() {
			handlers[k] = h
		}
	}

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
	owner := fmt.Sprintf("%s/%d", host, os.Getpid())
	// Tell the capacity planner what this worker can do.
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			if err := capacity.Announce(ctx, pool, owner, capacity.Worker{Proxies: len(proxies), IntervalS: interval.Seconds(), At: time.Now()}); err != nil && ctx.Err() == nil {
				log.Warn("announcing capacity failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	serveMetrics(ctx, *metricsAddr, reg, log)
	// Every minute: fill rates and proxy health; every 15 minutes: canaries;
	// daily: the website client version.
	go func() {
		minute := time.NewTicker(time.Minute)
		canary := time.NewTicker(15 * time.Minute)
		daily := time.NewTicker(24 * time.Hour)
		defer minute.Stop()
		defer canary.Stop()
		defer daily.Stop()
		detect := func() {
			if v, err := fetch.Client.DetectClientVersion(ctx); err != nil {
				log.Warn("detecting the website client version failed; the last known one stays", "err", err)
			} else {
				log.Info("website client version", "version", v)
			}
		}
		detect()
		for {
			select {
			case <-ctx.Done():
				return
			case <-minute.C:
				if err := rec.Flush(ctx, pool, time.Now()); err != nil && ctx.Err() == nil {
					log.Warn("saving fill rates failed", "err", err)
				}
				if err := monitor.Pool(ctx, pool, reg, owner, pp.Stats(), time.Now()); err != nil && ctx.Err() == nil {
					log.Warn("saving proxy health failed", "err", err)
				}
			case <-canary.C:
				monitor.Canary(ctx, pool, fetch, rec, reg, log)
			case <-daily.C:
				detect()
			}
		}
	}()
	w := &worker.Worker{Pool: pool, Owner: owner, Handlers: handlers, OnResult: monitor.Jobs(reg),
		Concurrency: *workers, Lease: 15 * time.Minute, Log: log}
	log.Info("worker running", "proxies", len(proxies), "interval", interval.String(), "workers", *workers, "push", wsCfg.Enabled())
	return w.Run(ctx)
}

// websubCmd runs the push receiver (behind your HTTPS reverse proxy) and
// keeps every listed channel subscribed.
func websubCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("websub", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "address the receiver listens on (put HTTPS in front of it)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := webSubConfig()
	if err != nil {
		return err
	}
	if !cfg.Enabled() {
		return fmt.Errorf("push is off: set %s (and the secret file) once the subdomain exists (D23)", EnvWebSubCallback)
	}
	keyer, err := privacy.LoadKeyer(pathFromEnv(EnvPrivacyKeyFile, DefaultPrivacyKeyFile))
	if err != nil {
		return fmt.Errorf("privacy key: %w", err)
	}
	sup := privacy.NewSuppressor(keyer)
	if err := sup.Refresh(ctx, pool); err != nil {
		return err
	}
	live := &kwstore.Live{}
	if _, err := live.Refresh(ctx, pool); err != nil {
		return err
	}
	go live.Run(ctx, pool, time.Minute, func(err error) { log.Warn("refreshing keywords failed", "err", err) })
	rc := &websub.Receiver{Pool: pool, Cfg: cfg, Log: log,
		RSS: &detect.RSS{Pool: pool, Write: &writer.Writer{Pool: pool, Sup: sup, KW: live, Media: fileExists(storagePath())}, Log: log}}
	sub := &websub.Subscriber{Pool: pool, Cfg: cfg, Log: log}
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := sub.Ensure(ctx); err != nil && ctx.Err() == nil {
				log.Warn("adding push subscriptions failed", "err", err)
			} else if n > 0 {
				log.Info("push subscriptions queued", "channels", n)
			}
			if err := sup.Refresh(ctx, pool); err != nil && ctx.Err() == nil {
				log.Warn("refreshing the suppression list failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	srv := &http.Server{Addr: *addr, Handler: rc, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("push receiver listening", "addr", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// serveMetrics serves /metrics on addr until ctx ends ("" = off).
func serveMetrics(ctx context.Context, addr string, reg *metrics.Registry, log *slog.Logger) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", reg.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("metrics endpoint stopped", "addr", addr, "err", err)
		}
	}()
}
