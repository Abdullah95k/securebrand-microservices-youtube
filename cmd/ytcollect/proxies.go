package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const proxiesUsage = `ytcollect proxies: the proxy IPs' health, as the workers saved it

An IP blocked on page requests at least 3 times a day, 3 days in a row, is
quarantined: no job uses it until it is released. The quarantine outlives a
restart. Release an IP once its provider has replaced it, or when you are sure
the blocks have passed; the worker picks a release up within a minute.

  ytcollect proxies                       every IP: state, rest, quarantine, requests, page and player blocks
                                          (counts since the worker started; player blocks never quarantine)
  ytcollect proxies release ID            release one quarantined IP (its ID from the list)
  ytcollect proxies release -all          release every quarantined IP

IDs are opaque (a hash of the proxy's address); no address or password is shown.
`

// proxiesCmd lists the proxies' health or releases quarantined IPs.
func proxiesCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string, now time.Time) error {
	switch {
	case len(args) == 0:
		return proxiesList(ctx, pool, out, now)
	case args[0] == "release" && len(args) == 2:
		where, arg := `proxy_key = $1`, any(args[1])
		if args[1] == "-all" {
			where, arg = `$1::text IS NOT NULL`, "all"
		}
		tag, err := pool.Exec(ctx, `UPDATE proxy_health SET released_at = now()
			WHERE quarantined_since IS NOT NULL AND (released_at IS NULL OR released_at < quarantined_since) AND `+where, arg)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if args[1] == "-all" {
				fmt.Fprintln(out, "no IP is quarantined")
				return nil
			}
			return fmt.Errorf("proxies: %s is not a quarantined IP (see ytcollect proxies)", args[1])
		}
		fmt.Fprintf(out, "%d IP(s) released; the worker puts them back within a minute\n", tag.RowsAffected())
		return nil
	}
	fmt.Fprint(out, proxiesUsage)
	return fmt.Errorf("proxies: unknown arguments %q", args)
}

func proxiesList(ctx context.Context, pool *pgxpool.Pool, out io.Writer, now time.Time) error {
	rows, err := pool.Query(ctx, `SELECT proxy_key, subnet, state, cooldown_until, quarantined_since,
			released_at IS NOT NULL AND quarantined_since IS NOT NULL AND released_at >= quarantined_since,
			requests_24h, blocks_24h, player_blocks, updated_at
		FROM proxy_health ORDER BY proxy_key`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "IP\tSUBNET\tSTATE\tRESTING UNTIL\tQUARANTINED SINCE\tREQUESTS\tPAGE BLOCKS\tPLAYER BLOCKS\tSAVED")
	n := 0
	stamp := func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return t.UTC().Format("2006-01-02 15:04Z")
	}
	for rows.Next() {
		var key, subnet, state string
		var cool, since *time.Time
		var released bool
		var requests, blocks, player int64
		var saved time.Time
		if err := rows.Scan(&key, &subnet, &state, &cool, &since, &released, &requests, &blocks, &player, &saved); err != nil {
			return err
		}
		if cool != nil && !cool.After(now) {
			cool = nil
		}
		if released {
			state += " (released)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s ago\n", key, subnet, state, stamp(cool), stamp(since), requests,
			max(blocks-player, 0), player, now.Sub(saved).Round(time.Second))
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintln(out, "no proxy health saved yet (a worker saves it every minute)")
		return nil
	}
	return tw.Flush()
}
