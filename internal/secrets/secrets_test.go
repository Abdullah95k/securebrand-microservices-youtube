package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFileTrims(t *testing.T) {
	p := writeFile(t, "  value-123\n", 0o600)
	got, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "value-123" {
		t.Errorf("got %q", got)
	}
}

func TestReadFileRejectsOpenPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not enforced on windows")
	}
	p := writeFile(t, "topsecretvalue", 0o644)
	_, err := ReadFile(p)
	if !errors.Is(err, ErrTooOpen) {
		t.Fatalf("err = %v, want ErrTooOpen", err)
	}
	if strings.Contains(err.Error(), "topsecretvalue") {
		t.Errorf("error leaks the secret: %v", err)
	}
}

func TestReadFileEmpty(t *testing.T) {
	p := writeFile(t, " \n", 0o600)
	if _, err := ReadFile(p); err == nil {
		t.Fatal("want error for empty file")
	}
}

func TestReadFileMissing(t *testing.T) {
	if _, err := ReadFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestReadFileTooLarge(t *testing.T) {
	p := writeFile(t, strings.Repeat("a", maxSize+1), 0o600)
	if _, err := ReadFile(p); err == nil {
		t.Fatal("want error for oversized file")
	}
}

func TestReadLines(t *testing.T) {
	p := writeFile(t, "# comment\n\nfirst\n  second  \n#another\n", 0o600)
	got, err := ReadLines(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "first|second" {
		t.Errorf("got %q", got)
	}
}

func TestReadLinesNoEntries(t *testing.T) {
	p := writeFile(t, "# only a comment\n", 0o600)
	if _, err := ReadLines(p); err == nil {
		t.Fatal("want error when there are no entries")
	}
}

func TestFromEnv(t *testing.T) {
	p := writeFile(t, "abc\n", 0o600)
	t.Setenv("TEST_SECRET_FILE", p)
	got, err := FromEnv("TEST_SECRET_FILE")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc" {
		t.Errorf("got %q", got)
	}

	t.Setenv("TEST_SECRET_FILE", "")
	if _, err := FromEnv("TEST_SECRET_FILE"); err == nil || !strings.Contains(err.Error(), "TEST_SECRET_FILE") {
		t.Fatalf("want error naming the variable, got %v", err)
	}
}

func TestLinesFromEnv(t *testing.T) {
	p := writeFile(t, "a\nb\n", 0o600)
	t.Setenv("TEST_LINES_FILE", p)
	got, err := LinesFromEnv("TEST_LINES_FILE")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %q", got)
	}
}
