// Package dbtest gives each test its own migrated database. Set
// YT_TEST_DATABASE_URL to a database the tests may use (CI starts one);
// its role must be allowed to create databases. Without it the database
// tests are skipped, unless YT_REQUIRE_DB is set (as in CI), which makes
// them fail instead.
//
// Each test's database copies the collation of the URL's database, so a
// test database made with an ICU collation tests that collation. The URL
// may point at PgBouncer in transaction mode (REQ-023): nothing here, or in
// yts, sets a startup parameter or keeps session state between
// transactions. YT_TEST_TIMEZONE, when set, gives every test database that
// time zone, to show the code doesn't depend on the server's.
// YT_TEST_MAX_CONNS, when set, gives each test pool that many connections.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
)

// New returns a pool on a fresh database with every migration applied.
// The database is dropped when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.OpenConfig(ctx, Empty(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Empty creates a fresh database with nothing in it and returns the pool
// configuration for it (db.Config). Close every pool on it before the test
// ends: the database is dropped then.
func Empty(t testing.TB) *pgxpool.Config {
	t.Helper()
	url := os.Getenv("YT_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("YT_REQUIRE_DB") != "" {
			t.Fatal("YT_TEST_DATABASE_URL is not set, and YT_REQUIRE_DB requires the database tests")
		}
		t.Skip("YT_TEST_DATABASE_URL not set: skipping database test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "yt_t_" + hex.EncodeToString(b)
	admin, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	create, err := createSQL(ctx, admin, name)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, create); err != nil {
		admin.Close()
		t.Fatalf("creating the test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close()
	})
	if tz := os.Getenv("YT_TEST_TIMEZONE"); tz != "" {
		if _, err := admin.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s SET timezone = '%s'", pgx.Identifier{name}.Sanitize(), tz)); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := db.Config(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	// YT_TEST_MAX_CONNS lowers the pool size (db.PoolSize): at 1, code that
	// needs a second connection while holding one hangs, which a small
	// production pool would turn into a deadlock under load.
	if n, err := strconv.Atoi(os.Getenv("YT_TEST_MAX_CONNS")); err == nil && n > 0 {
		cfg.MaxConns = int32(n)
	}
	return cfg
}

// createSQL creates name from template0 with the encoding, locale provider
// and collation of the database q is connected to.
func createSQL(ctx context.Context, q *pgxpool.Pool, name string) (string, error) {
	var version int
	if err := q.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return "", err
	}
	locale := "NULL::text"
	switch {
	case version >= 170000:
		locale = "d.datlocale"
	case version >= 150000:
		locale = "d.daticulocale"
	}
	provider := "'c'"
	if version >= 150000 {
		provider = "d.datlocprovider::text"
	}
	var encoding, collate, ctype, prov string
	var loc *string
	if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT pg_encoding_to_char(d.encoding), d.datcollate, d.datctype, %s, %s
		FROM pg_database d WHERE d.datname = current_database()`, provider, locale)).Scan(&encoding, &collate, &ctype, &prov, &loc); err != nil {
		return "", err
	}
	lit := func(s string) string { return "'" + s + "'" }
	sql := fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 ENCODING %s LC_COLLATE %s LC_CTYPE %s",
		pgx.Identifier{name}.Sanitize(), lit(encoding), lit(collate), lit(ctype))
	switch {
	case prov == "i" && loc != nil:
		sql += " LOCALE_PROVIDER icu ICU_LOCALE " + lit(*loc)
	case prov == "b" && loc != nil:
		sql += " LOCALE_PROVIDER builtin BUILTIN_LOCALE " + lit(*loc)
	case version >= 150000:
		sql += " LOCALE_PROVIDER libc"
	}
	return sql, nil
}
