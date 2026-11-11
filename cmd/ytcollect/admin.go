package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/capacity"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/discovery"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/enrich"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/erasure"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/metrics"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/monitor"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ops"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/suggest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/usage"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
)

const channelsUsage = `Usage:
  ytcollect channels add [-kind core|mention] [-tier A|B|C|D|M] [-reason TEXT] <@handle|URL|UC…>...
  ytcollect channels import [-kind …] [-tier …] <file>   (one channel per line; # starts a comment)
  ytcollect channels list [-status listed|dormant|candidate|no_match|removed|all]
  ytcollect channels set -kind core|mention -tier A|B|C|D|M <UC…>
  ytcollect channels unlist [-reason TEXT] <UC…>
`

// channelsCmd runs "channels <sub> …".
func channelsCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string) error {
	if len(args) == 0 {
		return errors.New(channelsUsage)
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("channels "+sub, flag.ContinueOnError)
	kind := fs.String("kind", registry.Core, "core or mention")
	tier := fs.String("tier", "", "A, B, C, D (core) or M (mention); default C for core, M for mention")
	reason := fs.String("reason", "", "why (kept with the channel)")
	status := fs.String("status", "listed", "which channels to list (all = every status)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var t policy.Tier
	if *tier != "" {
		var err error
		if t, err = policy.ParseTier(*tier); err != nil {
			return err
		}
	}
	switch sub {
	case "add", "import":
		var inputs []string
		if sub == "import" {
			if fs.NArg() != 1 {
				return errors.New(channelsUsage)
			}
			var err error
			if inputs, err = readList(fs.Arg(0)); err != nil {
				return err
			}
		} else {
			inputs = fs.Args()
		}
		if len(inputs) == 0 {
			return errors.New(channelsUsage)
		}
		for _, in := range inputs {
			if err := registry.AddInput(ctx, pool, registry.Input{Input: in, Kind: *kind, Tier: t, Reason: *reason}); err != nil {
				return fmt.Errorf("%s: %w", in, err)
			}
		}
		fmt.Fprintf(out, "%d channel(s) queued; a worker resolves and lists them (see \"ytcollect health\").\n", len(inputs))
		return nil
	case "list":
		st := *status
		if st == "all" {
			st = ""
		}
		chans, err := registry.Channels(ctx, pool, st)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "CHANNEL\tHANDLE\tKIND\tTIER\tSTATUS\tADDED BY\tLAST CHECKED")
		for _, c := range chans {
			last := "never"
			if c.LastChecked != nil {
				last = c.LastChecked.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Handle, c.Kind, c.Tier, c.Status, c.AddedBy, last)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(out, "%d channel(s)\n", len(chans))
		return nil
	case "set":
		if fs.NArg() != 1 || t == "" {
			return errors.New(channelsUsage)
		}
		return registry.Set(ctx, pool, fs.Arg(0), *kind, t)
	case "unlist":
		if fs.NArg() != 1 {
			return errors.New(channelsUsage)
		}
		return registry.Unlist(ctx, pool, fs.Arg(0), *reason)
	}
	return errors.New(channelsUsage)
}

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
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// healthCmd prints the queue by kind and priority.
func healthCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	st, err := queue.Stats(ctx, pool)
	if err != nil {
		return err
	}
	sw, err := worker.LoadSwitches(ctx, pool)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PRIORITY\tKIND\tJOBS\tDUE NOW\tRUNNING\tFAILING\tDEAD\tOLDEST DUE\tSWITCH")
	for _, d := range st {
		state := "on"
		if sw["all"] || sw[d.Kind] {
			state = "OFF"
		}
		fmt.Fprintf(tw, "P%d\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n", d.Priority, d.Kind, d.Total, d.Due, d.Leased, d.Failing,
			d.Dead, d.Oldest.Round(time.Minute), state)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	var listed, inputsFailed int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM channels WHERE status = 'listed'`).Scan(&listed)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM channel_inputs WHERE error IS NOT NULL`).Scan(&inputsFailed)
	fmt.Fprintf(out, "\nListed channels: %d. Channel inputs that could not be resolved: %d.\n", listed, inputsFailed)
	if inputsFailed > 0 {
		rows, err := pool.Query(ctx, `SELECT input, error FROM channel_inputs WHERE error IS NOT NULL ORDER BY input LIMIT 20`)
		if err == nil {
			for rows.Next() {
				var in, e string
				if rows.Scan(&in, &e) == nil {
					fmt.Fprintf(out, "  %s: %s\n", in, e)
				}
			}
			rows.Close()
		}
	}
	return nil
}

// switchCmd turns a job kind (or "all") off or on.
func switchCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string) error {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return errors.New("usage: ytcollect switch <job kind|all> on|off")
	}
	if err := worker.SetSwitch(ctx, pool, args[0], args[1] == "off"); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is now %s (workers pick it up within 30 seconds)\n", args[0], args[1])
	return nil
}

// schedulerCmd runs the maintenance loop: partitions ahead of time and the
// official-API hit purge (daily), missing recurring jobs, the search
// rotation, the database size and the sent outbox events (hourly), the
// commenter graph (daily) and tier recompute (weekly).
func schedulerCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("scheduler", flag.ContinueOnError)
	metricsAddr := fs.String("metrics-addr", ":9102", "the ops port: /metrics, /healthz and /readyz (empty: off)")
	minStretch := fs.Float64("min-stretch", 1, "refresh tiers C, D and M at least this many times less often (1: only as the capacity planner needs; at most 8)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *minStretch < 1 || *minStretch > capacity.MaxStretch {
		return fmt.Errorf("scheduler: -min-stretch %v is not between 1 and %v", *minStretch, capacity.MaxStretch)
	}
	reg := metrics.New()
	health := &ops.Health{}
	health.Add("database", pool.Ping)
	serveOps(ctx, *metricsAddr, reg, health, log)
	// Outgoing webhooks: sent from here, every 10 seconds.
	wh, err := dispatcher(pool, log)
	if err != nil {
		return fmt.Errorf("webhooks: %w", err)
	}
	if wh != nil {
		log.Info("webhooks on", "endpoints", len(wh.Cfg.Endpoints))
		go wh.Run(ctx, 10*time.Second)
	}
	type task struct {
		name  string
		every time.Duration
		run   func() error
		last  time.Time
	}
	tasks := []*task{
		{name: "metrics", every: time.Minute, run: func() error { return monitor.DB(ctx, pool, reg, time.Now()) }},
		{name: "partitions", every: 24 * time.Hour, run: func() error { return db.EnsurePartitions(ctx, pool, time.Now(), 3) }},
		{name: "rearm", every: time.Hour, run: func() error {
			n, err := registry.Rearm(ctx, pool)
			if n > 0 {
				log.Info("missing jobs re-added", "jobs", n)
			}
			return err
		}},
		{name: "search terms", every: time.Hour, run: func() error {
			added, removed, err := discovery.SyncTerms(ctx, pool)
			if added+removed > 0 {
				log.Info("search rotation updated", "added", added, "removed", removed)
			}
			return err
		}},
		{name: "API data expiry", every: 24 * time.Hour, run: func() error {
			e, err := discovery.ExpireAPIData(ctx, pool, time.Now())
			if e != (discovery.APIExpiry{}) {
				log.Info("official-API data past 30 days handled (YouTube's API rule)", "hits_deleted", e.Hits,
					"video_api_fields_cleared", e.Videos, "candidates_removed", e.Candidates, "checks_moved_up", e.Prioritised)
			}
			return err
		}},
		{name: "commenter graph", every: 24 * time.Hour, run: func() error {
			_, err := discovery.CommenterGraph(ctx, pool)
			return err
		}},
		{name: "privacy audit", every: 24 * time.Hour, run: func() error {
			s, err := privacyService(ctx, pool, log)
			if err != nil {
				return err
			}
			res, err := s.Audit(ctx)
			if err != nil {
				return err
			}
			log.Info("privacy audit done", "suppressed_found_and_erased", res.Total())
			return db.SetSetting(ctx, pool, monitor.AuditKey, monitor.AuditResult{Violations: res.Total(), At: time.Now()})
		}},
		{name: "suppression list copy", every: time.Hour, run: func() error {
			return copySuppressionList(ctx, pool, log)
		}},
		{name: "archive", every: 24 * time.Hour, run: func() error {
			a, err := archiverOrNil(ctx, pool, log)
			if a == nil || err != nil {
				return err
			}
			_, err = a.Run(ctx)
			return err
		}},
		{name: "archive scrub", every: time.Hour, run: func() error {
			a, err := archiverOrNil(ctx, pool, log)
			if a == nil || err != nil {
				return err
			}
			_, err = a.Scrub(ctx)
			return err
		}},
		{name: "retention", every: 24 * time.Hour, run: func() error {
			r, err := privacy.LoadRetention(ctx, pool)
			if err != nil || r.Forever() && !r.RedactDeletedComments {
				return err // everything is kept (the default)
			}
			out, err := erasure.ApplyRetention(ctx, pool, r, time.Now())
			log.Info("retention settings applied", "rows", out)
			return err
		}},
		{name: "database size", every: time.Hour, run: func() error { return usage.SampleSize(ctx, pool, time.Now()) }},
		{name: "coverage roll-up", every: time.Hour, run: func() error {
			// One JobCoverage per job kind and finished hour (fix 12), from
			// the usage tables; hours that ended 10 minutes ago or more, so
			// the hour's last jobs are in; two days back, once each.
			now := time.Now()
			n, err := outbox.New(ctx, now).Coverage(ctx, pool, now.Add(-48*time.Hour), now.Add(-10*time.Minute))
			if n > 0 {
				log.Info("coverage roll-ups added to the outbox", "events", n)
			}
			return err
		}},
		{name: "outbox prune", every: time.Hour, run: func() error {
			// Sent events are copies made for sending: they go after 7 days
			// (the owner, 7 November). Unsent ones stay until sent.
			n, err := outbox.Prune(ctx, pool, time.Now().Add(-outbox.Keep))
			if n > 0 {
				log.Info("sent outbox events deleted", "rows", n)
			}
			return err
		}},
		{name: "capacity", every: time.Hour, run: func() error {
			r, err := capacity.PlanWith(ctx, pool, time.Now(), capacity.Options{MinStretch: *minStretch})
			if err == nil {
				log.Info("capacity plan", "supply_per_hour", int(r.SupplyPerHour), "demand_per_hour", int(r.DemandPerHour),
					"fixed_per_hour", int(r.FixedPerHour), "stretchable_per_hour", int(r.StretchablePerHour),
					"spare_time_per_hour", int(r.SpareTimePerHour), "spare_debt_hours", r.SpareDebtHours, "top_kinds", r.TopKinds(6),
					"stretch", r.Stretch, "min_stretch", *minStretch, "recommended_ips", r.RecommendedIPs, "debt_hours", r.DebtHours)
			}
			return err
		}},
		{name: "keyword suggestions", every: 24 * time.Hour, run: func() error {
			n, activated, err := suggest.Run(ctx, pool, time.Now())
			if err != nil {
				return err
			}
			ps, err := suggest.MeasurePrecision(ctx, pool, time.Now())
			low := 0
			for _, p := range ps {
				if p.Low {
					low++
				}
			}
			log.Info("keyword suggestions and precision", "suggested", n, "activated", activated, "low_precision_keywords", low)
			return err
		}},
		{name: "enrichment order", every: 24 * time.Hour, run: func() error {
			_, err := enrich.Prioritise(ctx, pool)
			return err
		}},
		{name: "tiers", every: 7 * 24 * time.Hour, run: func() error {
			n, err := discovery.RecomputeTiers(ctx, pool, time.Now())
			if n > 0 {
				log.Info("channel tiers recomputed", "moved", n)
			}
			return err
		}},
	}
	log.Info("scheduler running")
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		for _, t := range tasks {
			if time.Since(t.last) < t.every {
				continue
			}
			t.last = time.Now()
			if err := t.run(); err != nil && ctx.Err() == nil {
				log.Warn("scheduler task failed", "task", t.name, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
