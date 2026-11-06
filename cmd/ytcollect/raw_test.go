package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/rawstore"
)

func TestOpenRawStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvRawStorageFile, filepath.Join(dir, "missing.json"))
	if s, err := openRawStore(quietLog, 0.01, false, nil); err != nil || s != nil {
		t.Fatalf("no settings file: store %v, err %v; want off", s, err)
	}
	objects := filepath.Join(dir, "objects")
	settings := filepath.Join(dir, "raw_storage.json")
	if err := os.WriteFile(settings, []byte(`{"type": "dir", "dir": "`+objects+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRawStorageFile, settings)
	s, err := openRawStore(quietLog, 1, false, nil)
	if err != nil || s == nil {
		t.Fatalf("store %v, err %v", s, err)
	}
	stop := s.Start(5 * time.Second)
	ref := s.Capture(context.Background(), rawstore.Record{At: time.Now(), Endpoint: "browse"}, []byte(`{}`), "")
	stop()
	if !strings.HasPrefix(ref, "file:yts/keep/") {
		t.Fatalf("ref = %s", ref)
	}
	if _, err := os.Stat(filepath.Join(objects, strings.TrimPrefix(ref, "file:"))); err != nil {
		t.Errorf("object not written: %v", err)
	}
	if err := os.WriteFile(settings, []byte(`{"type": "s3", "endpoint": "http://example.com", "region": "x", "bucket": "b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRawStore(quietLog, 0.01, false, nil); err == nil {
		t.Error("bad settings must stop the worker")
	}
}
