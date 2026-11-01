package fixturescan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test inputs are assembled at run time so this file never contains a
// literal that the scanner (or gitleaks) would flag.
func join(parts ...string) string { return strings.Join(parts, "") }

func scan(t *testing.T, text string) []Finding {
	t.Helper()
	f, err := ScanReader("x.txt", strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestScanReader(t *testing.T) {
	realLookingChannel := join("U", "C", "abcdefghijklmnopqrstuv") // 24 chars
	fakeChannel := join(FakeChannelPrefix, "abcdefghijklmnopqr")   // 24 chars
	apiKey := join("AI", "za", strings.Repeat("x", 35))            // 39 chars
	proxyURL := join("http://", "alice", ":", "s3cret", "@", "10.0.0.1:8080")
	docProxyURL := join("http://", "user", ":", "password", "@host:port")
	email := join("someone", "@", "gmail", ".com")
	docEmail := join("someone", "@", "example", ".com")
	intlPhone := join("+", "964 770 123 4567")
	iraqPhone := join("0", "7701234567")

	tests := []struct {
		name string
		text string
		want []string // rule names, in order
	}{
		{"clean text", "nothing to see here 1,234,567 views UTC+3", nil},
		{"real-looking channel id", "channel " + realLookingChannel + " end", []string{"youtube-channel-id"}},
		{"channel id at line start", realLookingChannel, []string{"youtube-channel-id"}},
		{"fake channel id allowed", "channel " + fakeChannel, nil},
		{"longer token is not a channel id", "x" + realLookingChannel + "x", nil},
		{"api key", "key=" + apiKey, []string{"google-api-key"}},
		{"proxy password", "proxy " + proxyURL, []string{"credentials-in-url"}},
		{"documented placeholder allowed", docProxyURL, nil},
		{"email", "mail " + email, []string{"email-address"}},
		{"example email allowed", "mail " + docEmail, nil},
		{"international phone", "call " + intlPhone, []string{"phone-number"}},
		{"iraqi mobile", "call " + iraqPhone + ".", []string{"phone-number"}},
		{"percent is not a phone", "+50% headroom, +5/s, +60–140k", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scan(t, tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d findings %v, want %v", len(got), got, tc.want)
			}
			for i, f := range got {
				if f.Rule != tc.want[i] {
					t.Errorf("finding %d rule = %q, want %q", i, f.Rule, tc.want[i])
				}
				if strings.Contains(f.Hint, tc.text) && len(tc.text) > 4 {
					t.Errorf("hint leaks the full match: %q", f.Hint)
				}
			}
		})
	}
}

func TestRedact(t *testing.T) {
	if got := redact("abcdefgh"); got != "abcd…" {
		t.Errorf("redact = %q", got)
	}
	if got := redact("abc"); got != "***" {
		t.Errorf("redact short = %q", got)
	}
}

func TestScanTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.go", "package ok\n")
	write("testdata/bad.json", "{\"key\": \""+join("AI", "za", strings.Repeat("y", 35))+"\"}\n")
	write(".git/config", join("url = https://", "bob", ":", "pw", "@", "example.org/repo")+"\n") // skipped folder
	write("bin/tool", "\x00\x01binary")                                                          // skipped folder
	write("image.png", "\x89PNG\x00\x00binary data")                                             // binary file
	write("big.txt", strings.Repeat("a", maxFileSize+1))

	got, err := ScanTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2: %v", len(got), got)
	}
	if got[0].Path != "big.txt" || got[0].Rule != "file-too-large" {
		t.Errorf("first finding = %v", got[0])
	}
	if got[1].Path != filepath.Join("testdata", "bad.json") || got[1].Rule != "google-api-key" || got[1].Line != 1 {
		t.Errorf("second finding = %v", got[1])
	}
}
