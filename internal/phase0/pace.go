package phase0

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/keywords"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// The pace experiment (E1) measures how fast one IP can ask each endpoint
// before YouTube answers with a block signal. For every endpoint class it
// starts slow on a few proxies, steps the gap between requests down, and
// stops that class at the first block: going faster after a block is never
// tried (plan §3, "back off when blocked"). A blocked IP rests as usual.

// PaceOptions configures the pace experiment.
type PaceOptions struct {
	Classes         []proxypool.Class
	Steps           []time.Duration // gap between requests on one IP, slowest first
	PerStep         int             // requests per IP in each step
	Proxies         int             // test the first N proxies of the list
	StopAfterBlocks int             // block signals within a step that end the class
}

// DefaultPaceOptions tests every endpoint from one request per 10 s to one
// per second on 3 IPs, 20 requests per IP per step.
func DefaultPaceOptions() PaceOptions {
	return PaceOptions{
		Classes: []proxypool.Class{proxypool.ClassBrowse, proxypool.ClassNext, proxypool.ClassPlayer, proxypool.ClassSearch,
			proxypool.ClassRSS, proxypool.ClassWatch, proxypool.ClassCDN},
		Steps:           []time.Duration{10 * time.Second, 6 * time.Second, 4 * time.Second, 3 * time.Second, 2 * time.Second, 1500 * time.Millisecond, time.Second},
		PerStep:         20,
		Proxies:         3,
		StopAfterBlocks: 1,
	}
}

// PaceRecord is one step of the experiment (aggregates only).
type PaceRecord struct {
	At        time.Time `json:"at"`
	Run       string    `json:"run"`
	Class     string    `json:"class"`
	Kinds     string    `json:"kinds"` // proxy types tested, e.g. "isp"
	GapSec    float64   `json:"gap_sec"`
	Proxies   int       `json:"proxies"`
	Requests  int       `json:"requests"`
	OK        int       `json:"ok"`
	Blocks    int       `json:"blocks"`
	Errors    int       `json:"errors"`
	MedianMS  int64     `json:"median_ms"`
	P90MS     int64     `json:"p90_ms"`
	WireBytes int64     `json:"wire_bytes"`
	Stopped   bool      `json:"stopped"` // block signals ended the class at this step
}

// PaceTargets are what the requests ask for.
type PaceTargets struct {
	Channels []string // channel IDs
	Videos   []string // video IDs
	Queries  []string // search terms
}

// GatherPaceTargets resolves up to maxChannels channels from the list and
// takes up to 5 recent videos from each one's videos tab. Search terms come
// from the seed keywords. It uses the collector's normal pool and pace.
func GatherPaceTargets(ctx context.Context, col *Collector, inputs []string, maxChannels int, log *slog.Logger) (PaceTargets, error) {
	var t PaceTargets
	var cost Cost
	for _, in := range inputs {
		if len(t.Channels) >= maxChannels || ctx.Err() != nil {
			break
		}
		id, err := col.Resolve(ctx, in, &cost)
		if err != nil {
			continue
		}
		t.Channels = append(t.Channels, id)
		ch, err := col.Channel(ctx, id, &cost)
		if err != nil {
			continue
		}
		tab, ok := ch.Channel.Tabs["videos"]
		if !ok {
			continue
		}
		tr, err := col.Tab(ctx, id, tab.Params, time.Time{}, 1, &cost)
		if err != nil {
			continue
		}
		for i, v := range tr.Items {
			if i >= 5 {
				break
			}
			t.Videos = append(t.Videos, v.VideoID)
		}
	}
	t.Queries = seedQueries(20)
	if len(t.Channels) == 0 || len(t.Videos) == 0 {
		return t, errors.New("pace: could not gather channels and videos to ask for")
	}
	log.Info("pace targets ready", "channels", len(t.Channels), "videos", len(t.Videos), "queries", len(t.Queries))
	return t, ctx.Err()
}

// seedQueries returns up to n search terms: the first spelling of each seed
// keyword that is not marked ambiguous.
func seedQueries(n int) []string {
	kws, err := keywords.Seed()
	if err != nil {
		return []string{"العراق"}
	}
	var out []string
	for _, k := range kws {
		if len(out) >= n {
			break
		}
		if !k.Ambiguous && len(k.Variants) > 0 {
			out = append(out, k.Variants[0])
		}
	}
	return out
}

// RunPace builds a pool of the first o.Proxies proxies (no failover, so each
// IP is measured on its own, and no pool brake, since the experiment stops a
// class at its first block itself) and runs the experiment.
func RunPace(ctx context.Context, env *Env, t PaceTargets, o PaceOptions, log *slog.Logger) error {
	if len(o.Steps) == 0 || len(o.Classes) == 0 || o.PerStep <= 0 {
		return errors.New("pace: steps, classes and per-step must be set")
	}
	n := o.Proxies
	if n <= 0 || n > len(env.Proxies) {
		n = len(env.Proxies)
	}
	proxies := env.Proxies[:n]
	pool := proxypool.New(proxies, proxypool.Config{MinInterval: o.Steps[0], Jitter: 0.05, DisableBrake: true, ParkAfter: 1}, time.Now().UnixNano())
	client := innertube.New(innertube.Options{Pool: pool, BaseURL: env.BaseURL, CDNBase: env.BaseURL})
	kinds := map[string]bool{}
	for _, p := range proxies {
		kinds[string(p.Kind())] = true
	}
	var kindList []string
	for k := range kinds {
		kindList = append(kindList, k)
	}
	sort.Strings(kindList)
	run := time.Now().UTC().Format(time.RFC3339)
	defer func() {
		_ = env.Store.Append("pool_stats", PoolSnapshot{At: time.Now(), Command: "pace", Run: "pace-" + run, Stats: pool.Stats()})
	}()

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var rngMu sync.Mutex
	pick := func(list []string) string {
		rngMu.Lock()
		defer rngMu.Unlock()
		return list[rng.Intn(len(list))]
	}
	for _, cl := range o.Classes {
		send, err := paceRequest(client, cl, t, pick)
		if err != nil {
			return err
		}
		for _, gap := range o.Steps {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			pool.SetMinInterval(gap)
			rec := paceStep(ctx, send, n, o)
			rec.At, rec.Run, rec.Class, rec.Kinds, rec.GapSec, rec.Proxies = time.Now(), run, string(cl), strings.Join(kindList, ","), gap.Seconds(), n
			if err := env.Store.Append("pace_steps", rec); err != nil {
				return err
			}
			log.Info("pace step", "class", cl, "gap", gap.String(), "requests", rec.Requests, "blocks", rec.Blocks, "errors", rec.Errors, "median_ms", rec.MedianMS)
			if rec.Stopped {
				log.Warn("block signal: this endpoint is not tested faster", "class", cl, "gap", gap.String())
				break
			}
		}
	}
	return nil
}

// paceRequest returns a function that sends one request of class cl.
func paceRequest(c *innertube.Client, cl proxypool.Class, t PaceTargets, pick func([]string) string) (func(context.Context) (*innertube.Response, error), error) {
	need := func(list []string, what string) error {
		if len(list) == 0 {
			return fmt.Errorf("pace: no %s to test %s with", what, cl)
		}
		return nil
	}
	switch cl {
	case proxypool.ClassBrowse:
		return func(ctx context.Context) (*innertube.Response, error) {
			return c.Call(ctx, innertube.Browse, map[string]any{"browseId": pick(t.Channels)})
		}, need(t.Channels, "channels")
	case proxypool.ClassNext:
		return func(ctx context.Context) (*innertube.Response, error) {
			return c.Call(ctx, innertube.Next, map[string]any{"videoId": pick(t.Videos)})
		}, need(t.Videos, "videos")
	case proxypool.ClassPlayer:
		return func(ctx context.Context) (*innertube.Response, error) {
			return c.Call(ctx, innertube.Player, map[string]any{"videoId": pick(t.Videos)})
		}, need(t.Videos, "videos")
	case proxypool.ClassSearch:
		return func(ctx context.Context) (*innertube.Response, error) {
			return c.Call(ctx, innertube.Search, map[string]any{"query": pick(t.Queries)})
		}, need(t.Queries, "search terms")
	case proxypool.ClassRSS:
		return func(ctx context.Context) (*innertube.Response, error) {
			return c.Get(ctx, proxypool.ClassRSS, "/feeds/videos.xml?channel_id="+url.QueryEscape(pick(t.Channels)), nil)
		}, need(t.Channels, "channels")
	case proxypool.ClassWatch:
		return func(ctx context.Context) (*innertube.Response, error) {
			return c.Get(ctx, proxypool.ClassWatch, "/watch?v="+url.QueryEscape(pick(t.Videos)), nil)
		}, need(t.Videos, "videos")
	case proxypool.ClassCDN:
		return func(ctx context.Context) (*innertube.Response, error) {
			return c.Thumbnail(ctx, pick(t.Videos), "hqdefault")
		}, need(t.Videos, "videos")
	}
	return nil, fmt.Errorf("pace: unknown endpoint class %q", cl)
}

// paceStep sends o.PerStep requests per IP with n workers (one request in
// flight per IP; the pool keeps the gap) and stops early at the block limit.
func paceStep(ctx context.Context, send func(context.Context) (*innertube.Response, error), n int, o PaceOptions) PaceRecord {
	var rec PaceRecord
	var mu sync.Mutex
	var lat []float64
	remaining := n * o.PerStep
	stop := false
	var wg sync.WaitGroup
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if stop || remaining <= 0 || ctx.Err() != nil {
					mu.Unlock()
					return
				}
				remaining--
				mu.Unlock()

				r, err := send(ctx)

				mu.Lock()
				rec.Requests++
				if r != nil {
					rec.WireBytes += r.WireBytes
				}
				switch {
				case err == nil:
					rec.OK++
					lat = append(lat, float64(r.Elapsed.Milliseconds()))
				case errors.Is(err, proxypool.ErrParked), errors.Is(err, innertube.ErrBlocked):
					rec.Blocks++
					if rec.Blocks >= o.StopAfterBlocks {
						stop, rec.Stopped = true, true
					}
				default:
					rec.Errors++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	rec.MedianMS, rec.P90MS = int64(pct(lat, 50)), int64(pct(lat, 90))
	return rec
}
