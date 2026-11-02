package phase0

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/feeds"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
)

// Environment variables (plan §7.3: secrets come from files named in *_FILE).
const (
	EnvProxiesFile  = "YT_PROXIES_FILE"
	EnvAPIKeyFile   = "YT_API_KEY_FILE"
	EnvChannelsFile = "YT_CHANNELS_FILE"
	EnvKeywordsFile = "YT_KEYWORDS_FILE"
	EnvDataDir      = "YT_DATA_DIR"
	EnvBaseURL      = "YT_BASE_URL" // tests only; default https://www.youtube.com
)

// Default locations, used when the environment variable is not set (they
// match docs/phase0-runbook.md and plan §9.3).
const (
	DefaultProxiesFile  = "/etc/securebrand-youtube/proxies.txt"
	DefaultAPIKeyFile   = "/etc/securebrand-youtube/youtube_api_key"
	DefaultChannelsFile = "/etc/securebrand-youtube/phase0-channels.txt"
	// DefaultKeywordsFile holds your own search terms (brands, people,
	// topics), one per line. It is optional and stays on the server.
	DefaultKeywordsFile = "/etc/securebrand-youtube/phase0-keywords.txt"
	DefaultDataDir      = "/var/lib/securebrand-youtube/phase0"
)

// PathFromEnv returns the path in envVar, or def when it is not set.
func PathFromEnv(envVar, def string) string {
	if p := strings.TrimSpace(os.Getenv(envVar)); p != "" {
		return p
	}
	return def
}

// DataDir is the Phase 0 data folder.
func DataDir() string { return PathFromEnv(EnvDataDir, DefaultDataDir) }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Env is everything a command needs.
type Env struct {
	Store     *Store
	Proxies   []*proxypool.Proxy
	Pool      *proxypool.Pool
	Client    *innertube.Client
	Collector *Collector
	Feeds     *feeds.Fetcher
	API       *ytapi.Client // nil when no key is configured
	Channels  []string
	Keywords  []string // your own search terms; empty when there is no keywords file
	BaseURL   string
	run       string // labels this process's pool snapshots
}

// Setup reads configuration from the environment. poolCfg sets pacing.
func Setup(poolCfg proxypool.Config) (*Env, error) {
	store, err := OpenStore(DataDir())
	if err != nil {
		return nil, err
	}
	lines, err := secrets.ReadLines(PathFromEnv(EnvProxiesFile, DefaultProxiesFile))
	if err != nil {
		return nil, fmt.Errorf("proxy list: %w", err)
	}
	proxies, err := proxypool.ParseList(lines)
	if err != nil {
		return nil, err
	}
	env := &Env{Store: store, Proxies: proxies, BaseURL: os.Getenv(EnvBaseURL), run: time.Now().UTC().Format(time.RFC3339Nano)}
	env.Pool = proxypool.New(proxies, poolCfg, time.Now().UnixNano())
	env.Client = innertube.New(innertube.Options{Pool: env.Pool, BaseURL: env.BaseURL, CDNBase: env.BaseURL})
	env.Collector = &Collector{Client: env.Client, Store: store, Now: time.Now}
	env.Feeds = feeds.NewFetcher(env.Client)

	// The API key is optional (search and the accuracy check need it).
	if keyPath := PathFromEnv(EnvAPIKeyFile, DefaultAPIKeyFile); os.Getenv(EnvAPIKeyFile) != "" || fileExists(keyPath) {
		key, err := secrets.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("API key: %w", err)
		}
		ledger, err := ytapi.NewLedger(store.Dir()+"/api_quota.json", ytapi.DefaultDailyQuota)
		if err != nil {
			return nil, err
		}
		apiBase := ""
		if env.BaseURL != "" {
			apiBase = env.BaseURL + "/youtube/v3"
		}
		env.API = ytapi.New(key, apiBase, ledger)
	}

	if path := PathFromEnv(EnvKeywordsFile, DefaultKeywordsFile); os.Getenv(EnvKeywordsFile) != "" || fileExists(path) {
		env.Keywords, err = readList(path)
		if err != nil {
			return nil, fmt.Errorf("keywords file: %w", err)
		}
	}
	if path := PathFromEnv(EnvChannelsFile, DefaultChannelsFile); os.Getenv(EnvChannelsFile) != "" || fileExists(path) {
		env.Channels, err = readList(path)
		if err != nil {
			return nil, fmt.Errorf("channel list: %w", err)
		}
	}
	return env, nil
}

// readList reads one entry per line, skipping blanks and # comments.
func readList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("the list is empty")
	}
	return out, nil
}

// SavePoolStats appends the pool's counters for the report.
func (e *Env) SavePoolStats(command string) {
	_ = e.Store.Append("pool_stats", PoolSnapshot{At: time.Now(), Command: command, Run: e.run, Stats: e.Pool.Stats()})
}

// SavePoolStatsEvery saves pool statistics every interval until ctx ends, so
// a report taken during a long run already shows blocks and pauses.
func (e *Env) SavePoolStatsEvery(ctx context.Context, command string, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.SavePoolStats(command)
		}
	}
}
