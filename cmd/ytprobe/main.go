// Command ytprobe runs the Phase 0 measurements (docs/plan.md §9) on the
// server. See docs/phase0-runbook.md for the exact steps.
//
// Secrets are read from files, never from the command line. The locations
// can be changed with environment variables:
//
//	YT_PROXIES_FILE   proxy list, one per line (chmod 600); default /etc/securebrand-youtube/proxies.txt
//	YT_API_KEY_FILE   YouTube Data API key (chmod 600); default /etc/securebrand-youtube/youtube_api_key
//	YT_CHANNELS_FILE  channels to measure; default /etc/securebrand-youtube/phase0-channels.txt
//	YT_KEYWORDS_FILE  your own search terms, optional; default /etc/securebrand-youtube/phase0-keywords.txt
//	YT_ANTHROPIC_KEY_FILE  Anthropic API key for "label -ai" (chmod 600); default /etc/securebrand-youtube/anthropic_api_key
//	YT_DATA_DIR       results, kept (official-API data only for 30 days); default /var/lib/securebrand-youtube/phase0
//
// Commands: check, resolve, crawl, detect, resweep, accuracy, pace, discover,
// census, label, transcripts, report, cleanup.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ailabel"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
)

const usage = `ytprobe: Phase 0 measurement kit (docs/phase0-runbook.md)

Usage: ytprobe <command> [flags]

  check      test every proxy once, the API key (1 quota unit) and the channel list
  resolve    look up channels (the whole list, or the ones given) and say why any is not found
  crawl      measure channels: listing cost, fields, comments (E3, E5, E6, E13)
  detect     watch feeds and channel tabs for new uploads; runs until stopped (E8)
  resweep    re-read comments of crawled videos to find edits and deletions (E7)
  accuracy   compare scraped numbers with the official API (E4, ~1 unit per 50 videos)
  pace       step the request pace up per endpoint until the first block signal (E1)
  discover   search your keywords on the official API and the website; compare them (E9)
  census     profile channels that discover found and the list lacks (E10)
  label      label found videos relevant or not, to score keyword matching (E11): -ai lets the AI do it;
             without -ai you answer in the terminal (a spot check of the AI with -limit 30)
  transcripts check whether transcripts come back without anti-bot tokens (E12)
  report     print the aggregate report (safe to share: no IDs, names or text)
  cleanup    remove official-API data older than 30 days (YouTube's API rule; everything else is kept)

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
	case "resolve":
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		inputs := fs.Args()
		if len(inputs) == 0 {
			inputs = env.Channels
		}
		if !phase0.ResolveList(ctx, os.Stdout, env.Collector, inputs) {
			os.Exit(1)
		}
	case "crawl":
		o := phase0.DefaultCrawlOptions()
		days := fs.Int("window-days", 365, "backfill window in days")
		fs.IntVar(&o.VideosPerChan, "videos", o.VideosPerChan, "videos per channel checked in depth")
		fs.IntVar(&o.CommentLimit, "comments", o.CommentLimit, "maximum comments read per video (0 = all)")
		fs.IntVar(&o.Workers, "workers", o.Workers, "channels processed in parallel")
		fs.IntVar(&o.Limit, "limit", 0, "crawl only the first N channels of the list (0 = all); for quick tests")
		fs.IntVar(&o.MaxTabPages, "max-pages", o.MaxTabPages, "safety limit of pages per channel tab (30 videos or 48 Shorts a page)")
		noPlayer := fs.Bool("no-player", false, "skip the player endpoint (tags, category)")
		fs.Parse(args)
		o.Window = time.Duration(*days) * 24 * time.Hour
		o.WithPlayer = !*noPlayer
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		go env.SavePoolStatsEvery(ctx, "crawl", 10*time.Minute)
		err = phase0.Crawl(ctx, env.Collector, env.Channels, o, log)
		env.SavePoolStats("crawl")
	case "detect":
		o := phase0.DetectOptions{}
		fs.DurationVar(&o.RSSEvery, "rss-every", 15*time.Minute, "feed check interval per channel")
		fs.DurationVar(&o.TabEvery, "tab-every", 24*time.Hour, "channel tab check interval")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		ids := resolveAll(ctx, env, log)
		go env.SavePoolStatsEvery(ctx, "detect", 10*time.Minute)
		err = phase0.Detect(ctx, env.Collector, env.Feeds, ids, o, log)
		env.SavePoolStats("detect")
		if err == context.Canceled {
			err = nil
		}
	case "resweep":
		limit := fs.Int("comments", 0, "maximum comments read per video, 0 = all (use the crawl's value)")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		err = phase0.Resweep(ctx, env.Collector, *limit, log)
		env.SavePoolStats("resweep")
	case "accuracy":
		maxVideos := fs.Int("max", 200, "videos to compare")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		pruneAPI(env, log)
		if env.API == nil {
			fmt.Fprintln(os.Stderr, "accuracy needs YT_API_KEY_FILE")
			os.Exit(2)
		}
		err = phase0.Accuracy(ctx, env.Collector, env.API, *maxVideos, log)
		env.SavePoolStats("accuracy")
	case "pace":
		o := phase0.DefaultPaceOptions()
		classes := fs.String("classes", joinClasses(o.Classes), "endpoint classes to test, comma-separated")
		steps := fs.String("steps", joinDurations(o.Steps), "gaps between requests on one IP, slowest first")
		fs.IntVar(&o.PerStep, "per-step", o.PerStep, "requests per IP in each step")
		fs.IntVar(&o.Proxies, "proxies", o.Proxies, "test the first N proxies of the list")
		fs.IntVar(&o.StopAfterBlocks, "stop-after", o.StopAfterBlocks, "block signals in one step that end an endpoint's test")
		channels := fs.Int("channels", 10, "channels from the list to take test targets from")
		fs.Parse(args)
		var perr error
		if o.Classes, perr = parseClasses(*classes); perr == nil {
			o.Steps, perr = parseDurations(*steps)
		}
		if perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			os.Exit(2)
		}
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		t, gerr := phase0.GatherPaceTargets(ctx, env.Collector, env.Channels, *channels, log)
		if gerr != nil {
			err = gerr
			break
		}
		err = phase0.RunPace(ctx, env, t, o, log)
	case "discover":
		o := phase0.DefaultDiscoverOptions()
		seed := fs.Int("seed", 20, "seed keywords to search as well (first spelling of each); 0 = only your keywords file")
		fs.IntVar(&o.WebPages, "web-pages", o.WebPages, "website search pages per term")
		fs.IntVar(&o.APIPages, "api-pages", o.APIPages, "official API pages per term (100 quota units each)")
		fs.IntVar(&o.APIUnits, "api-units", o.APIUnits, "most quota units this run may use (0 = website only)")
		days := fs.Int("api-days", 30, "official API: only videos published in the last N days (0 = any time)")
		fs.StringVar(&o.WebParams, "web-params", "", "website search filter parameters (advanced)")
		fs.Parse(args)
		o.APIWithin = time.Duration(*days) * 24 * time.Hour
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		pruneAPI(env, log)
		o.Terms = append(append([]string{}, env.Keywords...), phase0.SeedTerms(*seed)...)
		api := env.API
		if o.APIUnits <= 0 {
			api = nil
		}
		go env.SavePoolStatsEvery(ctx, "discover", 10*time.Minute)
		err = phase0.Discover(ctx, env.Collector, api, o, log)
		env.SavePoolStats("discover")
	case "census":
		max := fs.Int("max", 500, "channels to profile")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		go env.SavePoolStatsEvery(ctx, "census", 10*time.Minute)
		err = phase0.Census(ctx, env.Collector, phase0.CensusOptions{Max: *max}, log)
		env.SavePoolStats("census")
	case "label":
		size := fs.Int("size", 300, "videos in the sample (half accepted, half rejected by the matcher)")
		reset := fs.Bool("reset", false, "draw a new sample and drop earlier answers")
		useAI := fs.Bool("ai", false, "let the AI label the sample (sends only the public titles to the AI provider)")
		model := fs.String("model", "", "with -ai: the Claude model ID to use (required)")
		effort := fs.String("effort", "high", "with -ai: effort level ("+strings.Join(ailabel.Efforts, ", ")+"; empty = the model's default)")
		batch := fs.Int("batch", 25, "with -ai: titles per request")
		fallback := fs.Bool("fallback", true, "with -ai: if the model declines, let the API answer with a fallback model")
		limit := fs.Int("limit", 0, "by hand: answer at most this many titles this time (0 = all)")
		fs.Parse(args)
		if *useAI {
			env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
			pruneAPI(env, log)
			key, kerr := secrets.ReadFile(phase0.PathFromEnv(phase0.EnvAIKeyFile, phase0.DefaultAIKeyFile))
			if kerr != nil {
				log.Error("Anthropic API key", "err", kerr)
				os.Exit(2)
			}
			client, cerr := ailabel.New(ailabel.Config{APIKey: key, Model: *model, Effort: *effort, Fallback: *fallback})
			if cerr != nil {
				fmt.Fprintln(os.Stderr, cerr)
				os.Exit(2)
			}
			err = phase0.LabelAI(ctx, env.Store, client, phase0.AILabelOptions{
				LabelOptions: phase0.LabelOptions{Size: *size, Terms: env.Keywords, Reset: *reset}, Batch: *batch}, log)
			break
		}
		stop() // Ctrl+C quits at once; every answer is already saved
		if fi, serr := os.Stdin.Stat(); serr == nil && fi.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintln(os.Stderr, "label needs your keyboard: run it with \"docker run -it ...\" (note the -it), or use -ai.")
			os.Exit(2)
		}
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		pruneAPI(env, log)
		err = phase0.Label(context.Background(), env.Store, os.Stdin, os.Stdout, phase0.LabelOptions{Size: *size, Terms: env.Keywords, Reset: *reset, Limit: *limit})
	case "transcripts":
		videos := fs.Int("videos", 30, "videos to check")
		channels := fs.Int("channels", 15, "channels from the list to take recent videos from")
		fs.Parse(args)
		env := mustSetup(proxypool.Config{MinInterval: *interval}, log)
		t, gerr := phase0.GatherPaceTargets(ctx, env.Collector, env.Channels, *channels, log)
		if gerr != nil {
			err = gerr
			break
		}
		ids := t.Videos
		if len(ids) > *videos {
			ids = ids[:*videos]
		}
		err = phase0.Transcripts(ctx, env.Collector, ids, log)
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
		n, cerr := phase0.PruneAPIData(store, time.Now())
		fmt.Printf("removed %d record(s) of official-API data older than 30 days; everything else is kept\n", n)
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
	env.Collector.Log = log
	return env
}

// pruneAPI drops official-API data older than 30 days (YouTube's API
// rule) before a command that reads or writes it. Everything else is kept.
func pruneAPI(env *phase0.Env, log *slog.Logger) {
	if n, err := phase0.PruneAPIData(env.Store, time.Now()); err != nil {
		log.Warn("removing old official-API data failed", "err", err)
	} else if n > 0 {
		log.Info("official-API data older than 30 days removed", "records", n)
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

func joinClasses(cs []proxypool.Class) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = string(c)
	}
	return strings.Join(parts, ",")
}

func parseClasses(s string) ([]proxypool.Class, error) {
	known := map[string]bool{}
	for _, c := range phase0.DefaultPaceOptions().Classes {
		known[string(c)] = true
	}
	var out []proxypool.Class
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !known[part] {
			return nil, fmt.Errorf("unknown endpoint class %q (known: %s)", part, joinClasses(phase0.DefaultPaceOptions().Classes))
		}
		out = append(out, proxypool.Class(part))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no endpoint classes given")
	}
	return out, nil
}

func joinDurations(ds []time.Duration) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.String()
	}
	return strings.Join(parts, ",")
}

func parseDurations(s string) ([]time.Duration, error) {
	var out []time.Duration
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		d, err := time.ParseDuration(part)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("bad step %q: use durations like 10s or 1.5s", part)
		}
		if len(out) > 0 && d >= out[len(out)-1] {
			return nil, fmt.Errorf("steps must get faster: %s is not shorter than %s", d, out[len(out)-1])
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no steps given")
	}
	return out, nil
}
