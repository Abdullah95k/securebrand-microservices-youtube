// Package db opens the Postgres database, applies the schema migrations
// embedded in this package, keeps the monthly partitions of the large time
// series ahead of time, and stores runtime settings.
//
// The connection URL contains a password, so it is read from a file
// (secrets.ReadFile) and never logged.
package db

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Partitioned lists the monthly-partitioned tables and their time column.
var Partitioned = []struct{ Table, Column string }{
	{"channel_snapshots", "taken_at"},
	{"video_snapshots", "taken_at"},
	{"post_snapshots", "taken_at"},
	{"comment_versions", "seen_at"},
	{"author_profiles", "fetched_at"},
}

// PoolSize is the most connections one process opens (REQ-023): the
// platform's PostgreSQL is reached through PgBouncer, with 50 server
// connections shared by every service.
const PoolSize = 4

// Config is the pool configuration for url, fit for PgBouncer in
// transaction mode (REQ-023): statements are described and cached on the
// client, never prepared on a server session (one transaction may run on
// a different server session than the last); no startup parameters; at
// most PoolSize connections; an idle connection is checked before use only
// after 5 minutes (each check is a query, REQ-024).
func Config(url string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The parser's message can contain the URL; keep the password out.
		return nil, errors.New("db: the database URL is not valid")
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe
	cfg.MaxConns = PoolSize
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ShouldPing = func(_ context.Context, p pgxpool.ShouldPingParams) bool { return p.IdleDuration > 5*time.Minute }
	return cfg, nil
}

// Open connects to url (Config).
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := Config(url)
	if err != nil {
		return nil, err
	}
	return OpenConfig(ctx, cfg)
}

// OpenConfig connects with cfg and checks the connection.
func OpenConfig(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connecting: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: connecting: %w", err)
	}
	return pool, nil
}

// Full reports whether err means the database has no connection slot
// free right now, which passes (REQ-023, REQ-024): PostgreSQL's too many
// connections (53300), not accepting connections yet (57P03) and
// insufficient resources (53000); PgBouncer's query_wait_timeout, "server
// login has been failing" (its cached copy too) and "no more connections
// allowed". A wrong password, or anything else, is not.
func Full(err error) bool {
	if err == nil {
		return false
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "53300", "57P03", "53000":
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"query_wait_timeout", "server login has been failing", "no more connections allowed"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// WaitWhileFull runs fn until it succeeds, fails for a reason other than a
// full database (Full), or wait has passed; between attempts it waits 1
// second, doubling up to 15, and logs each wait.
func WaitWhileFull(ctx context.Context, wait time.Duration, log *slog.Logger, what string, fn func(context.Context) error) error {
	deadline := time.Now().Add(wait)
	pause := time.Second
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil || !Full(err) || ctx.Err() != nil || time.Now().Add(pause).After(deadline) {
			return err
		}
		if log != nil {
			log.Warn("the database has no free connection; waiting", "for", what, "attempt", attempt, "retry_in", pause.String(),
				"gives_up_in", time.Until(deadline).Round(time.Second).String(), "err", err)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(pause):
		}
		pause = min(2*pause, 15*time.Second)
	}
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("db: migration file %q must be named NNNN_name.sql", name)
		}
		data, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("db: two migrations numbered %d", out[i].version)
		}
	}
	return out, nil
}

// migrateLock keeps two processes from applying a migration at once.
const migrateLock = "securebrand-youtube-migrate"

// Migrate applies the migrations not applied yet, each in its own
// transaction. Each transaction first takes a transaction-level advisory
// lock and checks the migration is still missing, so two processes never
// apply one twice; the lock ends with the transaction, which PgBouncer's
// transaction pooling allows (a session lock would stay with a shared
// server session, REQ-023). It then creates the partitions for this month
// and the next 3.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	locked := func(fn func(tx pgx.Tx) error) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, migrateLock); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	if err := locked(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`)
		return err
	}); err != nil {
		return nil, err
	}
	done := map[int]bool{}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		done[v] = true
	}
	var applied []string
	for _, m := range ms {
		if done[m.version] {
			continue
		}
		ran := false
		err := locked(func(tx pgx.Tx) error {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, m.version).Scan(&exists); err != nil || exists {
				return err // another process applied it meanwhile
			}
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			ran = true
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name)
			return err
		})
		if err != nil {
			return applied, fmt.Errorf("db: migration %s: %w", m.name, err)
		}
		if ran {
			applied = append(applied, m.name)
		}
	}
	if err := EnsurePartitions(ctx, pool, time.Now(), 3); err != nil {
		return applied, err
	}
	return applied, nil
}

// partitionLock keeps two processes from creating a partition at once
// (CREATE TABLE IF NOT EXISTS isn't safe when two run together).
const partitionLock = "securebrand-youtube-partitions"

// partitionTx runs fn in a transaction holding the partition lock (a
// transaction-level advisory lock, which PgBouncer's transaction pooling
// allows). A table another process created meanwhile ("already exists",
// 42P07) counts as done.
func partitionTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, partitionLock); err != nil {
			return err
		}
		return fn(tx)
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "42P07" {
		return nil
	}
	return err
}

// tableExists reads the catalog with a plain query (the statement's own
// snapshot), which sees a table another process created and committed
// while this one waited for the lock; to_regclass may not.
func tableExists(ctx context.Context, q Querier, name string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = $1 AND n.nspname = current_schema())`, name).Scan(&ok)
	return ok, err
}

func ensureDefault(ctx context.Context, pool *pgxpool.Pool, table string) error {
	return partitionTx(ctx, pool, func(tx pgx.Tx) error {
		if ok, err := tableExists(ctx, tx, table+"_default"); err != nil || ok {
			return err
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_default PARTITION OF %s DEFAULT`, table, table))
		return err
	})
}

// EnsurePartitions creates, for every partitioned table, a default
// partition and the monthly partitions from the month of now up to ahead
// months later. It is safe to run repeatedly (the scheduler runs it daily),
// and in several processes at once.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time, ahead int) error {
	start := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, p := range Partitioned {
		if err := ensureDefault(ctx, pool, p.Table); err != nil {
			return fmt.Errorf("db: default partition of %s: %w", p.Table, err)
		}
		for i := 0; i <= ahead; i++ {
			from := start.AddDate(0, i, 0)
			if err := createMonth(ctx, pool, p.Table, from); err != nil {
				return err
			}
		}
	}
	return nil
}

// EnsureMonth creates table's partition for the month that starts at
// month (rows of that month in the default partition move into it). The
// archiver uses it for old months, which EnsurePartitions never creates.
func EnsureMonth(ctx context.Context, pool *pgxpool.Pool, table string, month time.Time) error {
	known := false
	for _, p := range Partitioned {
		known = known || p.Table == table
	}
	if !known {
		return fmt.Errorf("db: %s is not a partitioned table", table)
	}
	if err := ensureDefault(ctx, pool, table); err != nil {
		return err
	}
	m := month.UTC()
	return createMonth(ctx, pool, table, time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC))
}

// PartitionName is the name of table's partition for the month of t.
func PartitionName(table string, t time.Time) string {
	return fmt.Sprintf("%s_y%04dm%02d", table, t.Year(), int(t.Month()))
}

func createMonth(ctx context.Context, pool *pgxpool.Pool, table string, from time.Time) error {
	name := PartitionName(table, from)
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	to := from.AddDate(0, 1, 0)
	// Rows that landed in the default partition for this month must move
	// into the new partition, or creating it fails: do both in one
	// transaction, under the partition lock, checking again under it.
	return partitionTx(ctx, pool, func(tx pgx.Tx) error {
		if ok, err := tableExists(ctx, tx, name); err != nil || ok {
			return err
		}
		lo, hi := from.Format(time.RFC3339), to.Format(time.RFC3339)
		col := ""
		for _, p := range Partitioned {
			if p.Table == table {
				col = p.Column
			}
		}
		tmp := name + "_moving"
		if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TEMP TABLE %s ON COMMIT DROP AS
			SELECT * FROM %s_default WHERE %s >= '%s' AND %s < '%s'`, tmp, table, col, lo, col, hi)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s_default WHERE %s >= '%s' AND %s < '%s'`, table, col, lo, col, hi)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`, name, table, lo, hi)); err != nil {
			return fmt.Errorf("db: partition %s: %w", name, err)
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s`, table, tmp))
		return err
	})
}

// GetSetting decodes the setting key into v; ok is false when it is not set.
func GetSetting(ctx context.Context, q Querier, key string, v any) (ok bool, err error) {
	var raw []byte
	err = q.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, v)
}

// SetSetting stores v as the setting key.
func SetSetting(ctx context.Context, q Querier, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, raw)
	return err
}

// Querier is a pool, a connection or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
