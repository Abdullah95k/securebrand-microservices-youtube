// Command ytcollect runs the production collector (docs/plan.md §8). Each
// service is a sub-command of this one binary, so one image serves every
// container in the Docker Compose file.
//
// Secrets are read from files, never from the command line:
//
//	YT_DATABASE_URL_FILE  Postgres URL with its password (chmod 600); default /etc/securebrand-youtube/database_url
//	YT_PRIVACY_KEY_FILE   secret for keyed hashes (openssl rand -hex 32; chmod 600; back it up);
//	                      default /etc/securebrand-youtube/privacy_key
//	YT_PROXIES_FILE       the worker's proxies, one per line (chmod 600); default /etc/securebrand-youtube/proxies.txt
//	YT_DEBUG_DIR          parser diagnostics (scrubbed skeletons); default /var/lib/securebrand-youtube/debug
//	YT_WEBSUB_CALLBACK    public https URL of the push receiver; push is off while unset (D23)
//	YT_WEBSUB_SECRET_FILE push signing secret (openssl rand -hex 32); default /etc/securebrand-youtube/websub_secret
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
)

// Environment variables naming secret files, and their defaults.
const (
	EnvDatabaseURLFile     = "YT_DATABASE_URL_FILE"
	DefaultDatabaseURLFile = "/etc/securebrand-youtube/database_url"
	EnvPrivacyKeyFile      = "YT_PRIVACY_KEY_FILE"
	DefaultPrivacyKeyFile  = "/etc/securebrand-youtube/privacy_key"
)

const usage = `ytcollect: SecureBrand YouTube collector

Usage: ytcollect <command> [flags]

  migrate    create or update the database tables (safe to run again)
  channels   add, import, list, set or unlist channels ("ytcollect channels" for details)
  keywords   import the seed list; add, list, set or remove keywords ("ytcollect keywords" for details)
  health     the job queue by kind and priority, and switched-off features
  erase      record an erasure request (author, channel, video, comment or post); a worker carries it out
  erasures   list erasure requests and their status
  privacy-audit  check that nothing erased is in the database (the scheduler runs it daily)
  export     write comments as JSON lines (-pseudonymize replaces author identities with keyed hashes)
  switch     turn a job kind (or "all") off or on: ytcollect switch comments off
  scheduler  run the maintenance loop (partitions, missing jobs); a long-running service
  worker     run the collector: lease jobs and read YouTube through the proxies; a long-running service
  websub     run the push-notification receiver (needs YT_WEBSUB_CALLBACK, D23); a long-running service

Run "ytcollect <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var err error
	switch cmd {
	case "migrate":
		fs.Parse(args)
		pool := mustDB(ctx, log)
		defer pool.Close()
		var applied []string
		applied, err = db.Migrate(ctx, pool)
		if err == nil {
			log.Info("database up to date", "applied", strings.Join(applied, ","))
		}
	case "channels", "keywords", "health", "switch", "scheduler", "worker", "websub", "erase", "erasures", "privacy-audit", "export":
		pool := mustDB(ctx, log)
		defer pool.Close()
		switch cmd {
		case "channels":
			err = channelsCmd(ctx, pool, os.Stdout, args)
		case "keywords":
			err = keywordsCmd(ctx, pool, os.Stdout, args)
		case "health":
			err = healthCmd(ctx, pool, os.Stdout)
		case "switch":
			err = switchCmd(ctx, pool, os.Stdout, args)
		case "scheduler":
			err = schedulerCmd(ctx, pool, log)
		case "worker":
			err = workerCmd(ctx, pool, log, args)
		case "websub":
			err = websubCmd(ctx, pool, log, args)
		case "erase":
			err = eraseCmd(ctx, pool, log, os.Stdout, args)
		case "erasures":
			err = erasuresCmd(ctx, pool, os.Stdout)
		case "privacy-audit":
			err = privacyAuditCmd(ctx, pool, log, os.Stdout)
		case "export":
			err = exportCmd(ctx, pool, log, args)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("command failed", "command", cmd, "err", err)
		os.Exit(1)
	}
}

func pathFromEnv(env, def string) string {
	if p := strings.TrimSpace(os.Getenv(env)); p != "" {
		return p
	}
	return def
}

func mustDB(ctx context.Context, log *slog.Logger) *pgxpool.Pool {
	url, err := secrets.ReadFile(pathFromEnv(EnvDatabaseURLFile, DefaultDatabaseURLFile))
	if err != nil {
		log.Error("database URL", "err", err)
		os.Exit(2)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := db.Open(cctx, url, "")
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(2)
	}
	return pool
}
