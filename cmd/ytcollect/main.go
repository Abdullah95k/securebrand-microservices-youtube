// Command ytcollect runs the production collector (docs/plan.md §8). Each
// service is a sub-command of this one binary, so one image serves every
// container in the Docker Compose file.
//
// Secrets are read from files, never from the command line:
//
//	YT_DATABASE_URL_FILE  Postgres URL with its password (chmod 600); default /etc/securebrand-youtube/database_url
//	YT_PRIVACY_KEY_FILE   secret for keyed hashes (openssl rand -hex 32; chmod 600; back it up);
//	                      default /etc/securebrand-youtube/privacy_key
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
  switch     turn a job kind (or "all") off or on: ytcollect switch comments off
  scheduler  run the maintenance loop (partitions, missing jobs); a long-running service

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
	case "channels", "keywords", "health", "switch", "scheduler":
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
