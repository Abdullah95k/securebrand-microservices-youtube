package db_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
)

func credentials(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, v := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func base() map[string]string {
	return map[string]string{
		"POSTGRES_HOST": "pg.fake.internal", "POSTGRES_PORT": "6543", "POSTGRES_DATABASE": "yts",
		"POSTGRES_USER": "yts", "POSTGRES_PASSWORD": "owner-secret",
		"POSTGRES_APP_USER": "yts_app", "POSTGRES_APP_PASSWORD": `app 'pass\word`,
		"POSTGRES_RO_USER": "yts_ro", "POSTGRES_RO_PASSWORD": "ro-secret",
	}
}

func parse(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("the connection string doesn't parse")
	}
	return cfg
}

func TestPlatformDSNRoles(t *testing.T) {
	dir := credentials(t, base())
	for role, want := range map[db.Role][2]string{
		db.RoleOwner:    {"yts", "owner-secret"},
		db.RoleApp:      {"yts_app", `app 'pass\word`},
		db.RoleReadOnly: {"yts_ro", "ro-secret"},
	} {
		dsn, err := db.PlatformDSN(dir, role)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		c := parse(t, dsn).ConnConfig
		if c.User != want[0] || c.Password != want[1] {
			t.Errorf("%s: user %s; the password didn't round-trip", role, c.User)
		}
		if c.Host != "pg.fake.internal" || c.Port != 6543 || c.Database != "yts" {
			t.Errorf("%s: %s:%d/%s", role, c.Host, c.Port, c.Database)
		}
		if c.TLSConfig == nil {
			t.Errorf("%s: no TLS (the default must be require)", role)
		}
	}
	// No read-only role in the secret: the app role reads instead.
	files := base()
	delete(files, "POSTGRES_RO_USER")
	delete(files, "POSTGRES_RO_PASSWORD")
	dsn, err := db.PlatformDSN(credentials(t, files), db.RoleReadOnly)
	if err != nil || parse(t, dsn).ConnConfig.User != "yts_app" {
		t.Errorf("read-only fallback: %v", err)
	}
	if _, err := db.PlatformDSN(dir, db.Role("admin")); err == nil {
		t.Error("an unknown role must be refused")
	}
}

func TestPlatformDSNTLS(t *testing.T) {
	cases := []struct {
		host, mode string
		ok         bool
	}{
		{"pg.fake.internal", "", true},
		{"pg.fake.internal", "require", true},
		{"pg.fake.internal", "verify-full", true},
		{"pg.fake.internal", "disable", false},
		{"pg.fake.internal", "prefer", false},
		{"pg.fake.internal", "allow", false},
		{"pg.fake.internal", "bogus", false},
		{"127.0.0.1", "disable", true},
		{"localhost", "disable", true},
		{"/var/run/postgresql", "disable", true},
	}
	for _, c := range cases {
		files := base()
		files["POSTGRES_HOST"] = c.host
		if c.mode != "" {
			files["POSTGRES_SSLMODE"] = c.mode
		}
		_, err := db.PlatformDSN(credentials(t, files), db.RoleApp)
		if c.ok != (err == nil) {
			t.Errorf("host %s, sslmode %q: err = %v", c.host, c.mode, err)
		}
		if err != nil && (strings.Contains(err.Error(), "owner-secret") || strings.Contains(err.Error(), "pass\\word")) {
			t.Errorf("an error must not show a password: %v", err)
		}
	}
	// A CA file makes the default verify-full, checked against that CA.
	files := base()
	files["POSTGRES_SSLROOTCERT"] = "-----BEGIN CERTIFICATE-----\nnot a real certificate\n-----END CERTIFICATE-----"
	dir := credentials(t, files)
	dsn, err := db.PlatformDSN(dir, db.RoleApp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "sslmode='verify-full'") || !strings.Contains(dsn, "sslrootcert='"+filepath.Join(dir, "POSTGRES_SSLROOTCERT")+"'") {
		t.Error("with a CA file, the default must be verify-full against it")
	}
}

func TestPlatformDSNMissingAndOpenFiles(t *testing.T) {
	files := base()
	delete(files, "POSTGRES_HOST")
	if _, err := db.PlatformDSN(credentials(t, files), db.RoleApp); err == nil || !strings.Contains(err.Error(), "POSTGRES_HOST") {
		t.Errorf("missing host: %v", err)
	}
	files = base()
	delete(files, "POSTGRES_PASSWORD")
	if _, err := db.PlatformDSN(credentials(t, files), db.RoleOwner); err == nil {
		t.Error("missing owner password must be refused")
	}
	dir := credentials(t, base())
	if err := os.Chmod(filepath.Join(dir, "POSTGRES_APP_PASSWORD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PlatformDSN(dir, db.RoleApp); err == nil {
		t.Error("a password file others can read must be refused")
	}
	app, ro, err := db.PlatformRoles(credentials(t, base()))
	if err != nil || app != "yts_app" || ro != "yts_ro" {
		t.Errorf("roles = %s, %s, %v", app, ro, err)
	}
}

// Grant gives the app role rows but not the schema, and the read-only
// role SELECT, now and for tables made later.
func TestGrant(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	var super bool
	if err := pool.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil || !super {
		t.Skip("needs a superuser to create test roles")
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	app, ro := "t_app_"+hex.EncodeToString(b), "t_ro_"+hex.EncodeToString(b)
	for _, r := range []string{app, ro} {
		if _, err := pool.Exec(ctx, "CREATE ROLE "+r+" NOLOGIN"); err != nil {
			t.Fatal(err)
		}
		role := r
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), "DROP OWNED BY "+role)
			_, _ = pool.Exec(context.Background(), "DROP ROLE "+role)
		})
	}
	if err := db.Grant(ctx, pool, app, ro); err != nil {
		t.Fatal(err)
	}
	if err := db.Grant(ctx, pool, app, ro); err != nil {
		t.Fatalf("a second run must be harmless: %v", err)
	}
	// A table made after the grant (a later migration).
	if _, err := pool.Exec(ctx, `CREATE TABLE later_table (id bigserial PRIMARY KEY, v text)`); err != nil {
		t.Fatal(err)
	}
	as := func(role, sql string) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, sql)
			return err
		})
	}
	for _, q := range []string{
		`INSERT INTO settings (key, value) VALUES ('grant-test', '1')`,
		`UPDATE settings SET value = '2' WHERE key = 'grant-test'`,
		`DELETE FROM settings WHERE key = 'grant-test'`,
		`INSERT INTO later_table (v) VALUES ('x')`,
		`INSERT INTO parse_failures (endpoint, error) VALUES ('browse', 'x')`, // a bigserial sequence
	} {
		if err := as(app, q); err != nil {
			t.Errorf("app: %s: %v", q, err)
		}
	}
	for _, q := range []string{`CREATE TABLE app_made (id int)`, `DROP TABLE later_table`, `ALTER TABLE settings ADD COLUMN x int`} {
		if err := as(app, q); err == nil {
			t.Errorf("app must not change the schema: %s", q)
		}
	}
	for _, q := range []string{`SELECT count(*) FROM settings`, `SELECT count(*) FROM later_table`, `SELECT count(*) FROM comments`} {
		if err := as(ro, q); err != nil {
			t.Errorf("read-only: %s: %v", q, err)
		}
	}
	for _, q := range []string{`INSERT INTO settings (key, value) VALUES ('ro', '1')`, `DELETE FROM later_table`} {
		if err := as(ro, q); err == nil {
			t.Errorf("read-only must not write: %s", q)
		}
	}
}
