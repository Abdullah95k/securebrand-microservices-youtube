// Command ytprobe runs the Phase 0 measurements (docs/plan.md §9) on the
// server. See docs/phase0-runbook.md for the exact steps.
//
// Secrets are read from files, never from the command line. The locations
// can be changed with environment variables:
//
//	YT_PROXIES_FILE   proxy list, one per line (chmod 600); default /etc/securebrand-youtube/proxies.txt
//	YT_API_KEY_FILE   YouTube Data API key (chmod 600); default /etc/securebrand-youtube/youtube_api_key
//	YT_CHANNELS_FILE  channels to measure; default /etc/securebrand-youtube/phase0-channels.txt
//	YT_DATA_DIR       results, deleted after 14 days; default /var/lib/securebrand-youtube/phase0
//
// Commands: check, crawl, detect, resweep, accuracy, report, cleanup.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

const usage = `ytprobe: Phase 0 measurement kit (docs/phase0-runbook.md)

Usage: ytprobe <command> [flags]

  check      test every proxy once, the API key (1 quota unit) and the channel list
  crawl      measure channels: listing cost, fields, comments (E3, E5, E6, E13)
  detect     watch feeds and channel tabs for new uploads; runs until stopped (E8)
  resweep    re-read comments of crawled videos to find edits and deletions (E7)
  accuracy   compare scraped numbers with the official API (E4, ~1 unit per 50 videos)
  report     print the aggregate report (safe to share: no IDs, names or text)
  cleanup    delete Phase 0 data older than 14 days

Run "ytprobe <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	interval := fs.Duration("interval", 3*time.Second, "minimum gap between requests on one proxy IP")
	var err error
	switch cmd {
	case "check":
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		if !phase0.Check(ctx, os.Stdout, env.Proxies, env.BaseURL, env.API, env.Channels) {
			os.Exit(1)
		}
	case "crawl":
		o := phase0.DefaultCrawlOptions()
		days := fs.Int("window-days", 365, "backfill window in days")
		fs.IntVar(&o.VideosPerChan, "videos", o.VideosPerChan, "videos per channel checked in depth")
		fs.IntVar(&o.CommentLimit, "comments", o.CommentLimit, "maximum comments read per video")
		fs.IntVar(&o.Workers, "workers", o.Workers, "channels processed in parallel")
		noPlayer := fs.Bool("no-player", false, "skip the player endpoint (tags, category)")
		fs.Parse(args)
		o.Window = time.Duration(*days) * 24 * time.Hour
		o.WithPlayer = !*noPlayer
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		cleanup(env, log)
		err = phase0.Crawl(ctx, env.Collector, env.Channels, o, log)
		env.SavePoolStats("crawl")
	case "detect":
		o := phase0.DetectOptions{}
		fs.DurationVar(&o.RSSEvery, "rss-every", 15*time.Minute, "feed check interval per channel")
		fs.DurationVar(&o.TabEvery, "tab-every", 24*time.Hour, "channel tab check interval")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		cleanup(env, log)
		ids := resolveAll(ctx, env, log)
		err = phase0.Detect(ctx, env.Collector, env.Feeds, ids, o, log)
		env.SavePoolStats("detect")
		if err == context.Canceled {
			err = nil
		}
	case "resweep":
		limit := fs.Int("comments", 2000, "maximum comments read per video (use the crawl's value)")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		err = phase0.Resweep(ctx, env.Collector, *limit, log)
		env.SavePoolStats("resweep")
	case "accuracy":
		maxVideos := fs.Int("max", 200, "videos to compare")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		if env.API == nil {
			fmt.Fprintln(os.Stderr, "accuracy needs YT_API_KEY_FILE")
			os.Exit(2)
		}
		err = phase0.Accuracy(ctx, env.Collector, env.API, *maxVideos, log)
		env.SavePoolStats("accuracy")
	case "report":
		fs.Parse(args)
		store, serr := phase0.OpenStore(phase0.DataDir())
		if serr != nil {
			fmt.Fprintln(os.Stderr, serr)
			os.Exit(2)
		}
		err = phase0.Report(store, os.Stdout, time.Now())
	case "cleanup":
		fs.Parse(args)
		store, serr := phase0.OpenStore(phase0.DataDir())
		if serr != nil {
			fmt.Fprintln(os.Stderr, serr)
			os.Exit(2)
		}
		n, cerr := store.Cleanup(time.Now(), phase0.Retention)
		fmt.Printf("removed %d file(s) older than 14 days\n", n)
		err = cerr
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("command failed", "command", cmd, "err", err)
		os.Exit(1)
	}
}

func mustSetup(cfg proxypool.Config, log *slog.Logger) *phase0.Env {
	env, err := phase0.Setup(cfg)
	if err != nil {
		log.Error("setup failed", "err", err)
		os.Exit(2)
	}
	return env
}

func cleanup(env *phase0.Env, log *slog.Logger) {
	if n, err := env.Store.Cleanup(time.Now(), phase0.Retention); err == nil && n > 0 {
		log.Info("old Phase 0 data removed", "files", n)
	}
}

func resolveAll(ctx context.Context, env *phase0.Env, log *slog.Logger) []string {
	cache := map[string]string{}
	_, _ = env.Store.ReadJSON("resolved.json", &cache)
	var ids []string
	for _, in := range env.Channels {
		if id, ok := cache[in]; ok {
			ids = append(ids, id)
			continue
		}
		var cost phase0.Cost
		id, err := env.Collector.Resolve(ctx, in, &cost)
		if err != nil {
			log.Warn("channel not resolved; skipped", "err", err)
			continue
		}
		cache[in] = id
		ids = append(ids, id)
	}
	_ = env.Store.WriteJSON("resolved.json", cache)
	return ids
}
