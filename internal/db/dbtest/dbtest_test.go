package dbtest_test

import (
	"context"
	"os"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
)

// A test database sorts as the database it was made from (an ICU test
// database gives ICU test databases).
func TestCollationIsCopied(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	src, err := db.Open(ctx, os.Getenv("YT_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var want, got string
	const q = `SELECT format('%s/%s/%s/%s', d.datcollate, d.datctype, d.datlocprovider, pg_encoding_to_char(d.encoding))
		FROM pg_database d WHERE d.datname = current_database()`
	if err := src.QueryRow(ctx, q).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, q).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("test database %q, made from %q", got, want)
	}
	// And it sorts the same: "a" < "B" under ICU en-US, not byte-wise.
	var srcOrder, order bool
	_ = src.QueryRow(ctx, `SELECT 'a' < 'B'`).Scan(&srcOrder)
	_ = pool.QueryRow(ctx, `SELECT 'a' < 'B'`).Scan(&order)
	if order != srcOrder {
		t.Errorf("'a' < 'B' is %v in the test database, %v in its source", order, srcOrder)
	}
}

// YT_TEST_TIMEZONE reaches the test databases' sessions (through PgBouncer
// too: it is the database's setting, not a startup parameter).
func TestTimezone(t *testing.T) {
	tz := os.Getenv("YT_TEST_TIMEZONE")
	if tz == "" {
		t.Skip("YT_TEST_TIMEZONE not set")
	}
	pool := dbtest.New(t)
	var got string
	if err := pool.QueryRow(context.Background(), `SELECT current_setting('TimeZone')`).Scan(&got); err != nil || got != tz {
		t.Errorf("TimeZone %q (%v), want %q", got, err, tz)
	}
}
