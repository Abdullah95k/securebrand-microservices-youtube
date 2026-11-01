package phase0

import (
	"os"
	"path/filepath"
	"testing"
)

type rec struct {
	N int `json:"n"`
}

func TestReadAllSkipsRecordBeingWritten(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Two complete records, then half a record that a running crawl is
	// still writing: the report must read the two and ignore the rest.
	p := filepath.Join(s.Dir(), "x.jsonl")
	if err := os.WriteFile(p, []byte("{\"n\":1}\n{\"n\":2}\n{\"n\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAll[rec](s, "x")
	if err != nil {
		t.Fatalf("partial last record must not fail the read: %v", err)
	}
	if len(got) != 2 || got[0].N != 1 || got[1].N != 2 {
		t.Errorf("got %+v", got)
	}

	// A broken record in the middle is real corruption and is reported.
	if err := os.WriteFile(p, []byte("{\"n\":1}\nnot json\n{\"n\":3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAll[rec](s, "x"); err == nil {
		t.Error("want an error for a corrupt complete line")
	}

	// Missing file: no records, no error.
	if got, err := ReadAll[rec](s, "missing"); err != nil || got != nil {
		t.Errorf("missing file: %v %v", got, err)
	}
}

func TestPathFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	if DataDir() != DefaultDataDir {
		t.Errorf("DataDir() = %q, want the default", DataDir())
	}
	t.Setenv(EnvDataDir, "/tmp/elsewhere")
	if DataDir() != "/tmp/elsewhere" {
		t.Errorf("DataDir() = %q, want the environment value", DataDir())
	}
}
