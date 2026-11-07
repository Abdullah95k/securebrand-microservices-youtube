package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
)

func TestRoleFor(t *testing.T) {
	for cmd, want := range map[string]db.Role{
		"migrate": db.RoleOwner, "scheduler": db.RoleOwner, "archive": db.RoleOwner,
		"worker": db.RoleApp, "websub": db.RoleApp, "channels": db.RoleApp, "jobs": db.RoleApp, "erase": db.RoleApp,
		"report": db.RoleReadOnly, "health": db.RoleReadOnly, "erasures": db.RoleReadOnly, "export": db.RoleReadOnly,
		"outbox": db.RoleReadOnly,
	} {
		if got := roleFor(cmd); got != want {
			t.Errorf("roleFor(%s) = %s; want %s", cmd, got, want)
		}
	}
}

func TestDatabaseURLSource(t *testing.T) {
	dir := t.TempDir()
	url := filepath.Join(dir, "database_url")
	if err := os.WriteFile(url, []byte("host=postgres user=ytcollect dbname=ytcollect sslmode=disable password=fake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDatabaseURLFile, url)
	t.Setenv(EnvPostgresDir, "")
	if got, err := databaseURL(db.RoleApp); err != nil || got != "host=postgres user=ytcollect dbname=ytcollect sslmode=disable password=fake" {
		t.Errorf("URL file: %q, %v", got, err)
	}
	creds := filepath.Join(dir, "platform")
	if err := os.Mkdir(creds, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"POSTGRES_HOST": "pg.fake.internal", "POSTGRES_DATABASE": "yts",
		"POSTGRES_APP_USER": "yts_app", "POSTGRES_APP_PASSWORD": "fake"} {
		if err := os.WriteFile(filepath.Join(creds, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(EnvPostgresDir, creds)
	got, err := databaseURL(db.RoleApp)
	if err != nil || got == "" || got == "host=postgres user=ytcollect dbname=ytcollect sslmode=disable password=fake" {
		t.Errorf("the platform folder must replace the URL file: %v", err)
	}
	if _, err := databaseURL(db.RoleOwner); err == nil {
		t.Error("no owner credentials: the owner role must fail")
	}
}
