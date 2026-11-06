package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
)

// Role is which database role a command connects as (REQ-010 fix 17,
// standard I1: least privilege).
type Role string

// The platform gives yts three roles.
const (
	// RoleOwner owns the tables: migrations, and the scheduler and archive,
	// which create and move monthly partitions.
	RoleOwner Role = "owner"
	// RoleApp reads and writes rows, never the schema: the worker, the
	// push receiver and the admin commands that change data.
	RoleApp Role = "app"
	// RoleReadOnly only reads: reports and listings.
	RoleReadOnly Role = "ro"
)

// The files of the platform's credentials secret, one value each.
const (
	pgHost       = "POSTGRES_HOST"
	pgPort       = "POSTGRES_PORT"
	pgDatabase   = "POSTGRES_DATABASE"
	pgSSLMode    = "POSTGRES_SSLMODE"
	pgCACert     = "POSTGRES_CA_CERT"     // the server's CA (PEM), as the platform gives it
	pgSSLRoot    = "POSTGRES_SSLROOTCERT" // the same, under libpq's name (either works)
	pgUser       = "POSTGRES_USER"
	pgPassword   = "POSTGRES_PASSWORD"
	pgAppUser    = "POSTGRES_APP_USER"
	pgAppPass    = "POSTGRES_APP_PASSWORD"
	pgROUser     = "POSTGRES_RO_USER"
	pgROPassword = "POSTGRES_RO_PASSWORD"
)

// PlatformDSN builds a connection string for role from the platform's
// credential files in dir (the secret mounted as files). The read-only role
// falls back to the app role when the secret has none. Connections without
// TLS are refused unless the host is this machine. With the server's CA
// (POSTGRES_CA_CERT), the server is verified: its certificate against the
// CA and its name against the host (verify-full, even when the secret says
// require).
func PlatformDSN(dir string, role Role) (string, error) {
	read := func(name string, required bool) (string, error) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			if required {
				return "", fmt.Errorf("db: %s is missing from %s", name, dir)
			}
			return "", nil
		}
		return secrets.ReadFile(p)
	}
	host, err := read(pgHost, true)
	if err != nil {
		return "", err
	}
	database, err := read(pgDatabase, true)
	if err != nil {
		return "", err
	}
	port, err := read(pgPort, false)
	if err != nil {
		return "", err
	}
	if port == "" {
		port = "5432"
	}
	sslMode, err := read(pgSSLMode, false)
	if err != nil {
		return "", err
	}
	rootCert, hasRoot := "", false
	for _, name := range []string{pgCACert, pgSSLRoot} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			rootCert, hasRoot = filepath.Join(dir, name), true
			break
		}
	}
	if sslMode == "" {
		sslMode = "require"
	}
	if hasRoot && sslMode == "require" {
		sslMode = "verify-full"
	}
	switch sslMode {
	case "require", "verify-ca", "verify-full":
	case "disable", "allow", "prefer":
		if !local(host) {
			return "", fmt.Errorf("db: %s is %q: refusing a database connection to %s without TLS (use require, verify-ca or verify-full)",
				pgSSLMode, sslMode, host)
		}
	default:
		return "", fmt.Errorf("db: %s %q is not a PostgreSQL sslmode", pgSSLMode, sslMode)
	}
	userFile, passFile := pgAppUser, pgAppPass
	switch role {
	case RoleOwner:
		userFile, passFile = pgUser, pgPassword
	case RoleReadOnly:
		if ro, _ := read(pgROUser, false); ro != "" {
			userFile, passFile = pgROUser, pgROPassword
		}
	case RoleApp:
	default:
		return "", fmt.Errorf("db: unknown role %q", role)
	}
	user, err := read(userFile, true)
	if err != nil {
		return "", err
	}
	password, err := read(passFile, true)
	if err != nil {
		return "", err
	}
	parts := []string{
		"host=" + quote(host), "port=" + quote(port), "dbname=" + quote(database),
		"user=" + quote(user), "password=" + quote(password), "sslmode=" + quote(sslMode),
	}
	if hasRoot {
		parts = append(parts, "sslrootcert="+quote(rootCert))
	}
	return strings.Join(parts, " "), nil
}

// PlatformRoles returns the app and read-only role names from the secret
// (the read-only one may be empty), for Grant.
func PlatformRoles(dir string) (app, ro string, err error) {
	app, err = secrets.ReadFile(filepath.Join(dir, pgAppUser))
	if err != nil {
		return "", "", err
	}
	if _, serr := os.Stat(filepath.Join(dir, pgROUser)); serr == nil {
		if ro, err = secrets.ReadFile(filepath.Join(dir, pgROUser)); err != nil {
			return "", "", err
		}
	}
	return app, ro, nil
}

// quote writes a libpq keyword value: single quotes, with \ and ' escaped.
func quote(v string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `'`, `\'`) + "'"
}

// local reports whether host is this machine (a Unix socket or loopback).
func local(host string) bool {
	if strings.HasPrefix(host, "/") || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Grant gives the app role row access (no schema changes) and the
// read-only role SELECT, on every table and sequence in the current schema,
// and on those the current role creates later (new migrations, monthly
// partitions). The migrate command runs it after the migrations; it is safe
// to run again.
func Grant(ctx context.Context, pool *pgxpool.Pool, app, ro string) error {
	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return err
	}
	s := pgx.Identifier{schema}.Sanitize()
	var stmts []string
	if app != "" {
		a := pgx.Identifier{app}.Sanitize()
		stmts = append(stmts,
			"GRANT USAGE ON SCHEMA "+s+" TO "+a,
			"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "+s+" TO "+a,
			"GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA "+s+" TO "+a,
			"ALTER DEFAULT PRIVILEGES IN SCHEMA "+s+" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "+a,
			"ALTER DEFAULT PRIVILEGES IN SCHEMA "+s+" GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO "+a,
		)
	}
	if ro != "" {
		r := pgx.Identifier{ro}.Sanitize()
		stmts = append(stmts,
			"GRANT USAGE ON SCHEMA "+s+" TO "+r,
			"GRANT SELECT ON ALL TABLES IN SCHEMA "+s+" TO "+r,
			"ALTER DEFAULT PRIVILEGES IN SCHEMA "+s+" GRANT SELECT ON TABLES TO "+r,
		)
	}
	for _, q := range stmts {
		if _, err := pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("db: granting: %w", err)
		}
	}
	return nil
}
