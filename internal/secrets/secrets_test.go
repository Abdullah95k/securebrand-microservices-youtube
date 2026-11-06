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

// A Kubernetes secret volume with the pod's fsGroup: root owns the files,
// the fsGroup may read them (0440), and each name is a symlink through
// "..data" to a timestamped folder (REQ-010 fix 13).
func TestGroupReadableForOwnGroupOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no file modes on Windows")
	}
	ours := true
	saved := inGroup
	inGroup = func(int) bool { return ours }
	t.Cleanup(func() { inGroup = saved })

	dir := t.TempDir()
	stamp := filepath.Join(dir, "..2026_11_06_15_00_00.123456789")
	if err := os.Mkdir(stamp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stamp, "proxies.yaml"), []byte("http://user:pass@203.0.113.1:8080 type=isp\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(stamp), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "proxies.yaml")
	if err := os.Symlink(filepath.Join("..data", "proxies.yaml"), link); err != nil {
		t.Fatal(err)
	}
	if lines, err := ReadLines(link); err != nil || len(lines) != 1 {
		t.Fatalf("Kubernetes layout, our group: %v, %v", lines, err)
	}
	ours = false
	if _, err := ReadLines(link); !errors.Is(err, ErrTooOpen) {
		t.Errorf("group-readable by a group we aren't in: err = %v, want ErrTooOpen", err)
	}

	ours = true
	for mode, ok := range map[os.FileMode]bool{
		0o600: true, 0o400: true, 0o640: true, 0o440: true,
		0o660: false, 0o650: false, 0o604: false, 0o644: false, 0o606: false, 0o601: false,
	} {
		p := writeFile(t, "value", mode)
		_, err := ReadFile(p)
		if ok && err != nil {
			t.Errorf("mode %04o: %v; want accepted", mode, err)
		}
		if !ok && !errors.Is(err, ErrTooOpen) {
			t.Errorf("mode %04o: err = %v; want ErrTooOpen", mode, err)
		}
	}
}

func TestPermissionDeniedSaysWhy(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root user on a Unix system")
	}
	p := writeFile(t, "value", 0o000)
	if _, err := ReadFile(p); err == nil || !strings.Contains(err.Error(), "65532") {
		t.Errorf("err = %v; want a hint about the collector's user", err)
	}
}

func TestOwnGroupIsRecognised(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no file modes on Windows")
	}
	// A new file belongs to this process's group, so 0640 is accepted.
	p := writeFile(t, "value", 0o640)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if gid, ok := fileGroup(info); !ok || !inGroup(gid) {
		t.Skipf("the temp folder gives new files another group (%d)", gid)
	}
	if _, err := ReadFile(p); err != nil {
		t.Errorf("0640 in our own group: %v", err)
	}
}
