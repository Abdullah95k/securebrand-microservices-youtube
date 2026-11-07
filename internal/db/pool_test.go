package db_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
)

// The pool fits PgBouncer in transaction mode (REQ-023): statements are
// described, never prepared on a server session; no startup parameters;
// at most 4 connections.
func TestConfigFitsTransactionPooling(t *testing.T) {
	cfg, err := db.Config("postgres://yts@db.invalid:5432/yts?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeCacheDescribe {
		t.Errorf("query mode %v, want cache_describe", cfg.ConnConfig.DefaultQueryExecMode)
	}
	if cfg.MaxConns != 4 {
		t.Errorf("MaxConns %d, want 4", cfg.MaxConns)
	}
	if len(cfg.ConnConfig.RuntimeParams) != 0 {
		t.Errorf("startup parameters %v: PgBouncer refuses them", cfg.ConnConfig.RuntimeParams)
	}
	if _, err := db.Config("postgres://yts@[bad"); err == nil {
		t.Error("a bad URL must fail")
	} else if want := "db: the database URL is not valid"; err.Error() != want {
		t.Errorf("the error must not show the URL: %q", err)
	}
}

// Three processes migrating one database at once: each migration is
// applied exactly once, and all three finish.
func TestConcurrentMigrations(t *testing.T) {
	cfg := dbtest.Empty(t)
	ctx := context.Background()
	var mu sync.Mutex
	applied := map[string]int{}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, err := db.OpenConfig(ctx, cfg.Copy())
			if err != nil {
				errs <- err
				return
			}
			defer pool.Close()
			names, err := db.Migrate(ctx, pool)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			for _, n := range names {
				applied[n]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	pool, err := db.OpenConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 || n != len(applied) {
		t.Errorf("%d migrations recorded, %d applied", n, len(applied))
	}
	for name, times := range applied {
		if times != 1 {
			t.Errorf("%s applied %d times", name, times)
		}
	}
}

// A full database (no free slot) is waited for; a wrong password, or
// anything else, is not.
func TestFull(t *testing.T) {
	full := []error{
		&pgconn.PgError{Code: "53300", Message: "remaining connection slots are reserved for roles with the SUPERUSER attribute"},
		&pgconn.PgError{Code: "57P03", Message: "the database system is starting up"},
		&pgconn.PgError{Code: "53000", Message: "insufficient resources"},
		&pgconn.PgError{Code: "08P01", Message: "query_wait_timeout"},
		&pgconn.PgError{Code: "08P01", Message: "server login has been failing, try again later (server_login_retry)"},
		&pgconn.PgError{Code: "08P01", Message: "server login has been failing, cached error: connect failed (server_login_retry)"},
		&pgconn.PgError{Code: "08P01", Message: "no more connections allowed (max_client_conn)"},
		fmt.Errorf("db: connecting: %w", &pgconn.PgError{Code: "53300", Message: "sorry, too many clients already"}),
	}
	for _, err := range full {
		if !db.Full(err) {
			t.Errorf("%v: must be waited for", err)
		}
	}
	for _, err := range []error{
		&pgconn.PgError{Code: "28P01", Message: "password authentication failed for user \"yts\""},
		&pgconn.PgError{Code: "42P01", Message: "relation \"x\" does not exist"},
		errors.New("dial tcp: lookup db.invalid: no such host"),
		nil,
	} {
		if db.Full(err) {
			t.Errorf("%v: must fail at once", err)
		}
	}
}

func TestWaitWhileFull(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	calls := 0
	err := db.WaitWhileFull(ctx, time.Minute, quiet, "test", func(context.Context) error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "53300", Message: "sorry, too many clients already"}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("full twice, then free: err %v after %d calls", err, calls)
	}
	calls = 0
	wrong := &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}
	if err := db.WaitWhileFull(ctx, time.Minute, quiet, "test", func(context.Context) error { calls++; return wrong }); !errors.Is(err, wrong) || calls != 1 {
		t.Errorf("a wrong password: err %v after %d calls; must fail at once", err, calls)
	}
	calls = 0
	start := time.Now()
	err = db.WaitWhileFull(ctx, 2500*time.Millisecond, quiet, "test", func(context.Context) error {
		calls++
		return &pgconn.PgError{Code: "08P01", Message: "query_wait_timeout"}
	})
	if !db.Full(err) || calls < 2 || time.Since(start) > 5*time.Second {
		t.Errorf("full for good: err %v after %d calls and %v; must give up after the wait", err, calls, time.Since(start))
	}
}
