// Package fixturescan looks for things that must never be committed to this
// public repository: real-looking YouTube channel IDs, Google API keys,
// credentials inside URLs (proxy passwords), email addresses and phone numbers.
//
// Test fixtures must be generated or scrubbed. Fake channel IDs use the
// "UCfake" prefix so they are easy to tell apart from real ones.
package fixturescan

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FakeChannelPrefix marks a channel ID as fake (safe to commit).
const FakeChannelPrefix = "UCfake"

// maxFileSize is the largest file we scan. Bigger files are reported, because
// a large text file in this repo is most likely a captured response.
const maxFileSize = 2 << 20 // 2 MiB

// Finding is one suspicious match.
type Finding struct {
	Path string
	Line int
	Rule string
	// Hint is a redacted preview of the match, safe to print in CI logs.
	Hint string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s (%s)", f.Path, f.Line, f.Rule, f.Hint)
}

type rule struct {
	name string
	re   *regexp.Regexp
	// group is the submatch index holding the value to check (0 = whole match).
	group int
	// allowed reports whether a matched value is a known-safe placeholder.
	allowed func(value string) bool
}

var (
	// Channel IDs are "UC" + 22 URL-safe base64 characters. Go's regexp has no
	// look-behind, so the boundaries are matched explicitly.
	channelIDRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(UC[A-Za-z0-9_-]{22})(?:$|[^A-Za-z0-9_-])`)
	apiKeyRe    = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	urlCredRe   = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://([^\s/:@'"<>]+:[^\s/@'"<>]+)@`)
	emailRe     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})`)
	intlPhoneRe = regexp.MustCompile(`\+\d{1,3}[ -]?\d{2,4}[ -]?\d{3,4}[ -]?\d{3,4}\b`)
	iraqPhoneRe = regexp.MustCompile(`(?:^|\D)(07[3-9]\d{8})(?:$|\D)`)
)

// Placeholders that may appear in documentation.
var allowedURLCredentials = map[string]bool{
	"user:password":     true,
	"user:pass":         true,
	"username:password": true,
	"USER:PASSWORD":     true,
}

// Email domains reserved for documentation and tests (RFC 2606 / RFC 6761).
func allowedEmailDomain(domain string) bool {
	d := strings.ToLower(domain)
	for _, safe := range []string{"example.com", "example.org", "example.net", "example", "test", "invalid", "localhost"} {
		if d == safe || strings.HasSuffix(d, "."+safe) {
			return true
		}
	}
	return false
}

var rules = []rule{
	{name: "youtube-channel-id", re: channelIDRe, group: 1, allowed: func(v string) bool {
		return strings.HasPrefix(v, FakeChannelPrefix)
	}},
	{name: "google-api-key", re: apiKeyRe},
	{name: "credentials-in-url", re: urlCredRe, group: 1, allowed: func(v string) bool {
		return allowedURLCredentials[v]
	}},
	{name: "email-address", re: emailRe, group: 1, allowed: allowedEmailDomain},
	{name: "phone-number", re: intlPhoneRe},
	{name: "phone-number", re: iraqPhoneRe, group: 1},
}

// ScanReader scans text from r and reports findings using path as the label.
func ScanReader(path string, r io.Reader) ([]Finding, error) {
	var out []Finding
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxFileSize)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		for _, ru := range rules {
			for _, m := range ru.re.FindAllStringSubmatchIndex(text, -1) {
				start, end := m[2*ru.group], m[2*ru.group+1]
				if start < 0 {
					continue
				}
				value := text[start:end]
				if ru.allowed != nil && ru.allowed(value) {
					continue
				}
				out = append(out, Finding{Path: path, Line: line, Rule: ru.name, Hint: redact(value)})
			}
		}
	}
	return out, sc.Err()
}

// ScanTree walks root and scans every text file, skipping build output and
// version-control folders.
func ScanTree(root string) ([]Finding, error) {
	var out []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "vendor", "node_modules":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if info.Size() > maxFileSize {
			out = append(out, Finding{Path: rel, Line: 0, Rule: "file-too-large", Hint: fmt.Sprintf("%d bytes", info.Size())})
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(data) {
			return nil
		}
		found, err := ScanReader(rel, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		out = append(out, found...)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out, err
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

// redact keeps only the first four characters so CI logs never show a full
// secret or identifier.
func redact(v string) string {
	r := []rune(v)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:4]) + "…"
}
