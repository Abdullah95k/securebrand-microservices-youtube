// Command ytcollect runs the production collector (docs/plan.md §8). Each
// service is a sub-command of this one binary, so one image serves every
// container in the Docker Compose file.
//
// Secrets are read from files, never from the command line:
//
//	YT_DATABASE_URL_FILE  Postgres URL with its password (chmod 600); default /etc/securebrand-youtube/database_url
//	YT_POSTGRES_DIR       instead of the URL: the platform's credential files (POSTGRES_HOST, POSTGRES_APP_USER, …);
//	                      each command connects as the owner, app or read-only role, over TLS (REQ-010 fix 17)
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
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/version"
)

// Environment variables naming secret files, and their defaults.
const (
	EnvDatabaseURLFile     = "YT_DATABASE_URL_FILE"
	DefaultDatabaseURLFile = "/etc/securebrand-youtube/database_url"
	// The platform's credentials, one file per value (Kubernetes secret
	// platform-credentials). Set, it replaces the URL file.
	EnvPostgresDir        = "YT_POSTGRES_DIR"
	EnvPrivacyKeyFile     = "YT_PRIVACY_KEY_FILE"
	DefaultPrivacyKeyFile = "/etc/securebrand-youtube/privacy_key"
	// Object storage for media and the archive (media.Config). No file:
	// both are off.
	EnvStorageFile = "YT_STORAGE_FILE"
)

const mainUsage = `ytcollect: SecureBrand YouTube collector

Usage: ytcollect <command> [flags]

  migrate    create or update the database tables (safe to run again)
  channels   add, import, list, set or unlist channels ("ytcollect channels" for details)
  keywords   import the seed list; add, list, set or remove keywords ("ytcollect keywords" for details)
  health     the job queue by kind and priority, and switched-off features
  jobs       dead jobs (too many failures): list them, or requeue them once the cause is fixed
  report     measured cost per channel per day and in total, projected to 1,000 and 10,000 channels with the IPs needed
  outbox     events waiting for Kafka; "outbox check" maps them to the platform contract and lists any it refuses
  erase      record an erasure request (author, channel, video, comment or post); a worker carries it out
  erasures   list erasure requests and their status
  privacy-audit  check that nothing erased is in the database (the scheduler runs it daily)
  suppression    export the suppression list (keyed hashes), or import a copy after restoring a backup
  export     write comments as JSON lines (-pseudonymize replaces author identities with keyed hashes)
  archive    history older than 12 months in object storage: list, run, scrub, search, restore
  webhooks   outgoing events: list deliveries, test an endpoint, retry dead deliveries
  switch     turn a job kind (or "all") off or on: ytcollect switch comments off
  scheduler  run the maintenance loop (partitions, missing jobs); a long-running service
  worker     run the collector: lease jobs and read YouTube through the proxies; a long-running service
  websub     run the push-notification receiver (needs YT_WEBSUB_CALLBACK, D23); a long-running service
  relay      publish the outbox to Kafka: content.* (checked against the contract) and yt.*; a long-running service
  version    the release and commit this program was built from

Run "ytcollect <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, mainUsage)
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
		// The migrations wait for a free connection when the shared
		// database is full, for up to -wait (REQ-024): the deploy's hook
		// then runs once there is room instead of failing.
		wait := fs.Duration("wait", 45*time.Minute, "how long to wait for a free database connection when the database is full")
		fs.Parse(args)
		pool := mustDB(ctx, log, db.RoleOwner, *wait)
		defer pool.Close()
		err = db.WaitWhileFull(ctx, *wait, log, "the migrations", func(ctx context.Context) error {
			applied, err := db.Migrate(ctx, pool)
			if err != nil {
				return err
			}
			log.Info("database up to date", "applied", strings.Join(applied, ","))
			return grantPlatformRoles(ctx, pool, log)
		})
	case "channels", "keywords", "health", "jobs", "report", "outbox", "switch", "scheduler", "worker", "websub", "relay", "erase", "erasures", "privacy-audit", "export", "archive", "webhooks", "suppression":
		switch cmd {
		case "scheduler", "worker", "websub", "relay":
			log.Info("starting", "command", cmd, "version", version.Version, "revision", version.Revision)
		}
		// A long-running service waits up to 10 minutes for a free
		// connection at start-up; a command run by hand, a minute.
		wait := time.Minute
		switch cmd {
		case "scheduler", "worker", "websub", "relay":
			wait = 10 * time.Minute
		}
		pool := mustDB(ctx, log, roleFor(cmd), wait)
		defer pool.Close()
		switch cmd {
		case "channels":
			err = channelsCmd(ctx, pool, os.Stdout, args)
		case "keywords":
			err = keywordsCmd(ctx, pool, os.Stdout, args)
		case "health":
			err = healthCmd(ctx, pool, os.Stdout)
		case "jobs":
			err = jobsCmd(ctx, pool, os.Stdout, args)
		case "report":
			err = reportCmd(ctx, pool, os.Stdout, args, time.Now())
		case "outbox":
			err = outboxCmd(ctx, pool, os.Stdout, args)
		case "switch":
			err = switchCmd(ctx, pool, os.Stdout, args)
		case "scheduler":
			err = schedulerCmd(ctx, pool, log, args)
		case "worker":
			err = workerCmd(ctx, pool, log, args)
		case "websub":
			err = websubCmd(ctx, pool, log, args)
		case "relay":
			err = relayCmd(ctx, pool, log, args)
		case "erase":
			err = eraseCmd(ctx, pool, log, os.Stdout, args)
		case "erasures":
			err = erasuresCmd(ctx, pool, os.Stdout)
		case "privacy-audit":
			err = privacyAuditCmd(ctx, pool, log, os.Stdout)
		case "export":
			err = exportCmd(ctx, pool, log, args)
		case "archive":
			err = archiveCmd(ctx, pool, log, os.Stdout, args)
		case "webhooks":
			err = webhooksCmd(ctx, pool, log, os.Stdout, args)
		case "suppression":
			err = suppressionCmd(ctx, pool, os.Stdout, args)
		}
	case "version":
		fmt.Printf("ytcollect %s\n", version.String())
	case "-h", "--help", "help":
		fmt.Print(mainUsage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, mainUsage)
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

// mustDB connects as role, waiting up to wait while the database is full
// (db.Full: no free connection slot); any other failure ends the program.
func mustDB(ctx context.Context, log *slog.Logger, role db.Role, wait time.Duration) *pgxpool.Pool {
	url, err := databaseURL(role)
	if err != nil {
		log.Error("database settings", "err", err)
		os.Exit(2)
	}
	var pool *pgxpool.Pool
	err = db.WaitWhileFull(ctx, wait, log, "connecting", func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var err error
		pool, err = db.Open(cctx, url)
		return err
	})
	if err != nil {
		log.Error("database", "err", err, "role", string(role))
		os.Exit(2)
	}
	return pool
}

// databaseURL is the platform's credentials for role when YT_POSTGRES_DIR
// is set, else the one URL file (Docker Compose: one role for everything).
func databaseURL(role db.Role) (string, error) {
	if dir := strings.TrimSpace(os.Getenv(EnvPostgresDir)); dir != "" {
		return db.PlatformDSN(dir, role)
	}
	return secrets.ReadFile(pathFromEnv(EnvDatabaseURLFile, DefaultDatabaseURLFile))
}

// roleFor is the least role a command needs (REQ-010 fix 17). The
// scheduler and the archive own the tables because they create, detach and
// drop monthly partitions; listings and reports only read.
func roleFor(cmd string) db.Role {
	switch cmd {
	case "migrate", "scheduler", "archive":
		return db.RoleOwner
	case "report", "health", "erasures", "export", "outbox":
		return db.RoleReadOnly
	}
	return db.RoleApp
}

// grantPlatformRoles gives the app and read-only roles their access after
// the migrations, when the platform's credentials are used.
func grantPlatformRoles(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	dir := strings.TrimSpace(os.Getenv(EnvPostgresDir))
	if dir == "" {
		return nil
	}
	app, ro, err := db.PlatformRoles(dir)
	if err != nil {
		return err
	}
	if err := db.Grant(ctx, pool, app, ro); err != nil {
		return err
	}
	log.Info("roles granted", "app", app, "read_only", ro)
	return nil
}
