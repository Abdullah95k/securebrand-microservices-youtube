package main

import (
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

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/archive"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
)

const archiveUsage = `ytcollect archive: history older than 12 months, in compressed files in object storage

  archive list                      the archived months and their parts
  archive run                       archive every month that is due now (the scheduler does this daily)
  archive scrub                     remove rows erased since they were archived (the scheduler does this hourly)
  archive search -table T [-channel ID] [-video ID] [-author ID] [-comment ID] [-post ID] [-from YYYY-MM] [-to YYYY-MM]
                                    print matching archived rows as JSON lines
  archive restore -table T -month YYYY-MM [-days 30]
                                    put a month back into Postgres for a while (it is archived again afterwards)

Tables: ` + "channel_snapshots, video_snapshots, post_snapshots, comment_versions, author_profiles" + `
`

// archiver opens the archive (it needs object storage).
func archiver(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*archive.Archiver, error) {
	store, err := openStorage(log)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("the archive needs object storage: create " + storagePath() + " (see docs/production-runbook.md)")
	}
	keyer, err := privacy.LoadKeyer(pathFromEnv(EnvPrivacyKeyFile, DefaultPrivacyKeyFile))
	if err != nil {
		return nil, fmt.Errorf("privacy key: %w", err)
	}
	return &archive.Archiver{Pool: pool, Store: store, Sup: privacy.NewSuppressor(keyer), Log: log}, nil
}

func parseMonth(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("month %q: use YYYY-MM", s)
	}
	return t, nil
}

func archiveCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, out io.Writer, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(out, archiveUsage)
		return nil
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("archive "+sub, flag.ContinueOnError)
	table := fs.String("table", "", "archived table")
	ids := map[string]*string{}
	for _, k := range privacy.Kinds {
		ids[k] = fs.String(k, "", "only rows of this "+k+" ID")
	}
	from := fs.String("from", "", "first month (YYYY-MM)")
	to := fs.String("to", "", "last month (YYYY-MM)")
	month := fs.String("month", "", "month to restore (YYYY-MM)")
	days := fs.Int("days", 30, "days the restored month stays in Postgres")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := archiver(ctx, pool, log)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		parts, err := a.List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "TABLE\tMONTH\tPART\tROWS\tBYTES\tSCRUBBED")
		for _, p := range parts {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", p.Table, p.Month.Format("2006-01"), p.Part, p.Rows, p.Bytes, p.ScrubbedAt.UTC().Format(time.RFC3339))
		}
		return tw.Flush()
	case "run":
		res, err := a.Run(ctx)
		for _, r := range res {
			fmt.Fprintf(out, "%s %s: %d rows in %d parts (%d bytes), %d erased rows left out\n",
				r.Table, r.Month.Format("2006-01"), r.Rows, r.Parts, r.Bytes, r.Skipped)
		}
		if err == nil && len(res) == 0 {
			fmt.Fprintln(out, "nothing is due")
		}
		return err
	case "scrub":
		n, err := a.Scrub(ctx)
		fmt.Fprintf(out, "%d erased rows removed from the archive\n", n)
		return err
	case "search":
		f, err := parseMonth(*from)
		if err != nil {
			return err
		}
		t, err := parseMonth(*to)
		if err != nil {
			return err
		}
		q := archive.Query{Table: *table, From: f, To: t, IDs: map[string]string{}}
		for k, v := range ids {
			if s := strings.TrimSpace(*v); s != "" {
				q.IDs[k] = s
			}
		}
		n, err := a.Search(ctx, q, out)
		fmt.Fprintf(os.Stderr, "%d rows\n", n)
		return err
	case "restore":
		m, err := parseMonth(*month)
		if err != nil || m.IsZero() {
			return errors.New("restore needs -table and -month YYYY-MM")
		}
		if *days < 1 {
			return errors.New("-days must be at least 1")
		}
		n, err := a.Restore(ctx, *table, m, time.Now().Add(time.Duration(*days)*24*time.Hour))
		if err == nil {
			fmt.Fprintf(out, "%d rows of %s %s are back in Postgres for %d days\n", n, *table, m.Format("2006-01"), *days)
		}
		return err
	default:
		fmt.Fprint(out, archiveUsage)
		return fmt.Errorf("unknown archive command %q", sub)
	}
}

// archiverOrNil is the archive for the scheduler: nil (and no error) when
// no object storage is configured.
func archiverOrNil(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*archive.Archiver, error) {
	if !fileExists(storagePath()) {
		return nil, nil
	}
	return archiver(ctx, pool, log)
}
