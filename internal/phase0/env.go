package phase0

import (
	"bufio"
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
	EnvDataDir      = "YT_DATA_DIR"
	EnvBaseURL      = "YT_BASE_URL" // tests only; default https://www.youtube.com
)

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
	BaseURL   string
}

// Setup reads configuration from the environment. poolCfg sets pacing.
func Setup(poolCfg proxypool.Config) (*Env, error) {
	store, err := OpenStore(os.Getenv(EnvDataDir))
	if err != nil {
		return nil, err
	}
	lines, err := secrets.LinesFromEnv(EnvProxiesFile)
	if err != nil {
		return nil, fmt.Errorf("proxy list: %w", err)
	}
	proxies, err := proxypool.ParseList(lines)
	if err != nil {
		return nil, err
	}
	env := &Env{Store: store, Proxies: proxies, BaseURL: os.Getenv(EnvBaseURL)}
	env.Pool = proxypool.New(proxies, poolCfg, time.Now().UnixNano())
	env.Client = innertube.New(innertube.Options{Pool: env.Pool, BaseURL: env.BaseURL})
	env.Collector = &Collector{Client: env.Client, Store: store, Now: time.Now}
	env.Feeds = feeds.NewFetcher(env.Client)

	if os.Getenv(EnvAPIKeyFile) != "" {
		key, err := secrets.FromEnv(EnvAPIKeyFile)
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

	if path := os.Getenv(EnvChannelsFile); path != "" {
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
	_ = e.Store.Append("pool_stats", PoolSnapshot{At: time.Now(), Command: command, Stats: e.Pool.Stats()})
}
