package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/usage"
)

const reportUsage = `ytcollect report: what collection measurably costs (REQ-010 fix 5, D5)

Per channel per day and in total, from what every job recorded (requests,
bytes on the wire, blocks), projected to 1,000 and 10,000 channels with the
proxy IPs needed. Counts only: no names, IDs or text.

  ytcollect report                         the last 48 hours
  ytcollect report -hours 24
  ytcollect report -from 2026-11-10 -to 2026-11-12          whole days, Iraq time
  ytcollect report -from "2026-11-10 14:00" -to "2026-11-12 14:00"
  ytcollect report -channels 1000,10000,50000 -json -o report.json

Flags:
`

// reportCmd prints the usage report as Markdown (or JSON).
func reportCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string, now time.Time) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprint(out, reportUsage)
		fs.PrintDefaults()
	}
	hours := fs.Int("hours", 48, "the window: this many hours back from -to (ignored with -from)")
	from := fs.String("from", "", `start, Iraq time: "2026-11-10" (00:00) or "2026-11-10 14:00"`)
	to := fs.String("to", "", "end, Iraq time, the same forms (default: now)")
	gap := fs.Duration("gap", 0, "per-IP gap for the IP estimate (default: what the workers announced, else 2s)")
	channels := fs.String("channels", "1000,10000", "channel counts to project to, comma-separated")
	asJSON := fs.Bool("json", false, "print JSON instead of Markdown")
	file := fs.String("o", "", "write to this file instead of the screen")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("report: unexpected argument %q (flags only)", fs.Arg(0))
	}
	o := usage.Options{Gap: *gap}
	var err error
	o.To = now
	if *to != "" {
		if o.To, err = parseIraqTime(*to); err != nil {
			return fmt.Errorf("-to: %w", err)
		}
	}
	if *from != "" {
		if o.From, err = parseIraqTime(*from); err != nil {
			return fmt.Errorf("-from: %w", err)
		}
	} else {
		if *hours < 1 {
			return fmt.Errorf("-hours must be at least 1, not %d", *hours)
		}
		o.From = o.To.Add(-time.Duration(*hours) * time.Hour)
	}
	if !o.To.After(o.From) {
		return fmt.Errorf("-to (%s) must be after -from (%s)", *to, *from)
	}
	if *gap < 0 {
		return fmt.Errorf("-gap must not be negative")
	}
	for _, s := range strings.Split(*channels, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 {
			return fmt.Errorf("-channels: %q is not a channel count", s)
		}
		o.Targets = append(o.Targets, n)
	}
	r, err := usage.Build(ctx, pool, o)
	if err != nil {
		return err
	}
	var text []byte
	if *asJSON {
		if text, err = json.MarshalIndent(r, "", "  "); err != nil {
			return err
		}
		text = append(text, '\n')
	} else {
		text = []byte(r.Markdown())
	}
	if *file == "" {
		_, err = out.Write(text)
		return err
	}
	if err := os.WriteFile(*file, text, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "Report written to %s\n", *file)
	return nil
}

// parseIraqTime reads "2026-11-10", "2026-11-10 14:00" or RFC 3339; the
// first two are Iraq time.
func parseIraqTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.DateOnly, "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, usage.Iraq); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf(`%q is not a time: use "2026-11-10" or "2026-11-10 14:00" (Iraq time)`, s)
}
