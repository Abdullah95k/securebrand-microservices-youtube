package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/webhook"
)

// EnvWebhooksFile names the endpoints file (webhook.Config).
const EnvWebhooksFile = "YT_WEBHOOKS_FILE"

func webhooksPath() string { return pathFromEnv(EnvWebhooksFile, webhook.DefaultConfigFile) }

const webhooksUsage = `ytcollect webhooks: outgoing events (video.relevant, post.relevant, channel.listed, erasure)

  webhooks list                  deliveries per endpoint and status
  webhooks test -endpoint NAME   send a signed "ping" to an endpoint now
  webhooks retry [-endpoint NAME]
                                 queue dead deliveries again

Endpoints are configured in ` + webhook.DefaultConfigFile + ` (` + EnvWebhooksFile + `); the scheduler sends.
`

// dispatcher opens the endpoints file; nil (and no error) without one.
func dispatcher(pool *pgxpool.Pool, log *slog.Logger) (*webhook.Dispatcher, error) {
	cfg, err := webhook.Load(webhooksPath())
	if errors.Is(err, webhook.ErrNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &webhook.Dispatcher{Pool: pool, Cfg: cfg, Log: log}, nil
}

func webhooksCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, out io.Writer, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(out, webhooksUsage)
		return nil
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("webhooks "+sub, flag.ContinueOnError)
	name := fs.String("endpoint", "", "endpoint name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch sub {
	case "list":
		counts, err := webhook.Counts(ctx, pool)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ENDPOINT\tSTATUS\tDELIVERIES\tOLDEST EVENT")
		for _, c := range counts {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", c.Endpoint, c.Status, c.N, c.Oldest.UTC().Format(time.RFC3339))
		}
		return tw.Flush()
	case "test":
		d, err := dispatcher(pool, log)
		if err != nil {
			return err
		}
		if d == nil {
			return errors.New("no endpoints: create " + webhooksPath() + " (see docs/production-runbook.md)")
		}
		status, err := d.Test(ctx, *name)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s answered HTTP %d\n", *name, status)
		return nil
	case "retry":
		n, err := webhook.Retry(ctx, pool, *name)
		if err == nil {
			fmt.Fprintf(out, "%d dead deliveries queued again\n", n)
		}
		return err
	default:
		fmt.Fprint(out, webhooksUsage)
		return fmt.Errorf("unknown webhooks command %q", sub)
	}
}
