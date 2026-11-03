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

// Open connects to url. searchPath, when set, selects a schema (tests use
// one schema per test).
func Open(ctx context.Context, url, searchPath string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The parser's message can contain the URL; keep the password out.
		return nil, errors.New("db: the database URL is not valid")
	}
	if searchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
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

// Migrate applies the migrations not applied yet, each in its own
// transaction, under an advisory lock so two processes never migrate at
// once. It then creates the partitions for this month and the next 3.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('securebrand-youtube-migrate'))`); err != nil {
		return nil, err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('securebrand-youtube-migrate'))`) //nolint:errcheck
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	done := map[int]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		done[v] = true
	}
	rows.Close()
	var applied []string
	for _, m := range ms {
		if done[m.version] {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name)
			return err
		})
		if err != nil {
			return applied, fmt.Errorf("db: migration %s: %w", m.name, err)
		}
		applied = append(applied, m.name)
	}
	if err := EnsurePartitions(ctx, pool, time.Now(), 3); err != nil {
		return applied, err
	}
	return applied, nil
}

// EnsurePartitions creates, for every partitioned table, a default
// partition and the monthly partitions from the month of now up to ahead
// months later. It is safe to run repeatedly (the scheduler runs it daily).
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time, ahead int) error {
	start := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, p := range Partitioned {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_default PARTITION OF %s DEFAULT`, p.Table, p.Table)); err != nil {
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
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_default PARTITION OF %s DEFAULT`, table, table)); err != nil {
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
	// transaction.
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
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
