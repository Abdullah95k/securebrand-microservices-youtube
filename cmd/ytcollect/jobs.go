package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

const jobsUsage = `ytcollect jobs: dead jobs (REQ-010 fix 3)

A job is dead when it failed too often for reasons of its own (a page that
won't parse, an error YouTube gives for that item). Blocks and a pool with no
free IP never count. A dead job is not retried until it is requeued.

  ytcollect jobs dead                     how many are dead, by kind and reason
  ytcollect jobs requeue -kind KIND       bring back every dead job of a kind
  ytcollect jobs requeue -kind KIND -id ID   bring back one
  ytcollect jobs requeue -all             bring back every dead job
`

// jobsCmd lists or requeues dead jobs. It prints counts and reasons, never
// entity IDs (commenter IDs are personal data).
func jobsCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(out, jobsUsage)
		return nil
	}
	switch args[0] {
	case "dead":
		ds, err := queue.DeadJobs(ctx, pool)
		if err != nil {
			return err
		}
		if len(ds) == 0 {
			fmt.Fprintln(out, "No dead jobs.")
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tJOBS\tSINCE (UTC)\tREASON")
		var total int64
		for _, d := range ds {
			total += d.Count
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", d.Kind, d.Count, d.Oldest.UTC().Format("2006-01-02 15:04"), d.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%d dead jobs. Fix the cause, then: ytcollect jobs requeue -kind KIND\n", total)
		return nil
	case "requeue":
		fs := flag.NewFlagSet("jobs requeue", flag.ContinueOnError)
		kind := fs.String("kind", "", "only dead jobs of this kind")
		id := fs.String("id", "", "only this job (needs -kind)")
		all := fs.Bool("all", false, "every dead job")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		switch {
		case *all && (*kind != "" || *id != ""):
			return errors.New("give -all, or -kind (and -id), not both")
		case !*all && *kind == "":
			return errors.New("say which: -kind KIND, -kind KIND -id ID, or -all")
		case *id != "" && *kind == "":
			return errors.New("-id needs -kind")
		}
		n, err := queue.Requeue(ctx, pool, *kind, *id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d dead jobs requeued; they are due now.\n", n)
		return nil
	default:
		fmt.Fprint(out, jobsUsage)
		return fmt.Errorf("unknown jobs command %q", args[0])
	}
}
