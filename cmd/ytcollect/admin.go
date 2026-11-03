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

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
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
	fmt.Fprintln(tw, "PRIORITY\tKIND\tJOBS\tDUE NOW\tRUNNING\tFAILING\tOLDEST DUE\tSWITCH")
	for _, d := range st {
		state := "on"
		if sw["all"] || sw[d.Kind] {
			state = "OFF"
		}
		fmt.Fprintf(tw, "P%d\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n", d.Priority, d.Kind, d.Total, d.Due, d.Leased, d.Failing,
			d.Oldest.Round(time.Minute), state)
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

// schedulerCmd runs the maintenance loop: partitions ahead of time (daily)
// and missing recurring jobs (hourly).
func schedulerCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	partitions := time.NewTicker(24 * time.Hour)
	rearm := time.NewTicker(time.Hour)
	defer partitions.Stop()
	defer rearm.Stop()
	run := func(name string, f func() error) {
		if err := f(); err != nil && ctx.Err() == nil {
			log.Warn("scheduler task failed", "task", name, "err", err)
		}
	}
	doPartitions := func() error { return db.EnsurePartitions(ctx, pool, time.Now(), 3) }
	doRearm := func() error {
		n, err := registry.Rearm(ctx, pool)
		if n > 0 {
			log.Info("missing jobs re-added", "jobs", n)
		}
		return err
	}
	run("partitions", doPartitions)
	run("rearm", doRearm)
	log.Info("scheduler running")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-partitions.C:
			run("partitions", doPartitions)
		case <-rearm.C:
			run("rearm", doRearm)
		}
	}
}
