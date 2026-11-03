// Package dbtest gives each test its own migrated schema in a test
// database. Set YT_TEST_DATABASE_URL to a database the tests may use (CI
// starts one). Without it the database tests are skipped, unless
// YT_REQUIRE_DB is set (as in CI), which makes them fail instead.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
)

// New returns a pool whose search_path is a fresh schema with every
// migration applied. The schema is dropped when the test ends.
func New(t testing.TB) *pgxpool.Pool {
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
	schema := "t_" + hex.EncodeToString(b)
	admin, err := db.Open(ctx, url, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, url, schema)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
