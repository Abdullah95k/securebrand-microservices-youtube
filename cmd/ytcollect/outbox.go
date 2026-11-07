package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/abdullah95k/securebrand-contracts/conformance"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/contract"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
)

const outboxUsage = `ytcollect outbox: the events waiting for Kafka (REQ-010 fix 12)

  ytcollect outbox                 events not yet sent, by kind, and the oldest
  ytcollect outbox check [flags]   map unsent events to the platform contract (v1.2.0)
                                   and list any its checks refuse; changes nothing

Flags of check:
  -limit N   events to check, oldest first (default 5000)
  -canary    check as the canary does (allows test-only values)
`

// outboxCmd shows the outbox and checks it against the contract. Read-only.
func outboxCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string) error {
	if len(args) == 0 {
		return outboxStatus(ctx, pool, out)
	}
	switch args[0] {
	case "check":
		fs := flag.NewFlagSet("outbox check", flag.ContinueOnError)
		fs.SetOutput(out)
		limit := fs.Int("limit", 5000, "events to check, oldest first")
		canary := fs.Bool("canary", false, "check as the canary does")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return outboxCheck(ctx, pool, out, *limit, conformance.Options{Production: true, Canary: *canary})
	case "-h", "help":
		fmt.Fprint(out, outboxUsage)
		return nil
	}
	fmt.Fprint(out, outboxUsage)
	return fmt.Errorf("outbox: unknown subcommand %q", args[0])
}

func outboxStatus(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	rows, err := pool.Query(ctx, `SELECT kind, count(*) FILTER (WHERE published_at IS NULL),
			count(*) FILTER (WHERE published_at IS NOT NULL), min(created_at) FILTER (WHERE published_at IS NULL)
		FROM event_outbox GROUP BY 1 ORDER BY 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tUNSENT\tSENT (KEPT 7 DAYS)\tOLDEST UNSENT")
	var total int64
	for rows.Next() {
		var kind string
		var unsent, sent int64
		var oldest *time.Time
		if err := rows.Scan(&kind, &unsent, &sent, &oldest); err != nil {
			return err
		}
		age := "-"
		if oldest != nil {
			age = time.Since(*oldest).Round(time.Minute).String()
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", kind, unsent, sent, age)
		total += unsent
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d events waiting. The relay (fix 12, PR 4) sends them; until then they stay here.\n", total)
	return nil
}

func outboxCheck(ctx context.Context, pool *pgxpool.Pool, out io.Writer, limit int, opt conformance.Options) error {
	var live kwstore.Live
	if _, err := live.Refresh(ctx, pool); err != nil {
		return err
	}
	m := &contract.Mapper{Options: opt, Keywords: func() contract.KeywordIDs { return live.Matcher() }}
	topics := map[string]int{}
	type problem struct {
		id           int64
		kind, entity string
		err          error
	}
	var problems []problem
	checked := 0
	var after int64
	for checked < limit {
		batch, err := outbox.Unsent(ctx, pool, after, min(1000, limit-checked))
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			after = r.ID
			checked++
			msgs, err := m.Map(r)
			if err != nil {
				problems = append(problems, problem{r.ID, r.Kind, r.EntityID, err})
				continue
			}
			for _, msg := range msgs {
				topics[msg.Topic]++
			}
		}
	}
	names := make([]string, 0, len(topics))
	for t := range topics {
		names = append(names, t)
	}
	sort.Strings(names)
	fmt.Fprintf(out, "Checked %d unsent events against contract v1.2.0.\n", checked)
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TOPIC\tMESSAGES")
	for _, t := range names {
		fmt.Fprintf(tw, "%s\t%d\n", t, topics[t])
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(problems) == 0 {
		fmt.Fprintln(out, "\nEvery message passes the contract's checks.")
		return nil
	}
	fmt.Fprintf(out, "\n%d events would not be sent:\n", len(problems))
	for i, p := range problems {
		if i == 20 {
			fmt.Fprintf(out, "  … and %d more\n", len(problems)-20)
			break
		}
		why := "cannot be mapped"
		var rej *contract.Rejected
		if errors.As(p.err, &rej) {
			why = "refused by the contract"
		}
		fmt.Fprintf(out, "  event %d, %s %s (%s): %v\n", p.id, p.kind, p.entity, why, p.err)
	}
	return fmt.Errorf("outbox: %d of %d events would not be sent", len(problems), checked)
}
