// Package secrets reads credentials (API keys, proxy lists) from files.
//
// Rules from the plan (§7, §8.10): secrets live in files on the server, the
// file path is given in an environment variable ending in _FILE, files must
// be readable by their owner only, and secret values never appear in errors
// or logs.
package secrets

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// maxSize caps how much we read: a secret file is small.
const maxSize = 1 << 20

// ErrTooOpen means the file can be read by users other than its owner.
var ErrTooOpen = errors.New("file permissions are too open; run: chmod 600 <file>")

// FromEnv reads the secret whose file path is in the environment variable
// envVar, for example YT_API_KEY_FILE.
func FromEnv(envVar string) (string, error) {
	path, err := pathFromEnv(envVar)
	if err != nil {
		return "", err
	}
	return ReadFile(path)
}

// LinesFromEnv reads a line-based secret file (for example the proxy list)
// whose path is in envVar. See ReadLines.
func LinesFromEnv(envVar string) ([]string, error) {
	path, err := pathFromEnv(envVar)
	if err != nil {
		return nil, err
	}
	return ReadLines(path)
}

// ReadFile returns the file's content with surrounding whitespace removed.
func ReadFile(path string) (string, error) {
	data, err := readChecked(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return v, nil
}

// ReadLines returns the non-empty lines of the file, skipping lines that
// start with '#'. Surrounding whitespace is removed from each line.
func ReadLines(path string) ([]string, error) {
	data, err := readChecked(path)
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("secret file %s has no entries", path)
	}
	return out, nil
}

func pathFromEnv(envVar string) (string, error) {
	path := strings.TrimSpace(os.Getenv(envVar))
	if path == "" {
		return "", fmt.Errorf("environment variable %s is not set; set it to the path of the secret file", envVar)
	}
	return path, nil
}

func readChecked(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		// The error names the path, never the content.
		return nil, fmt.Errorf("opening secret file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking secret file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret file %s has mode %04o: %w", path, info.Mode().Perm(), ErrTooOpen)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading secret file %s: %w", path, err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("secret file %s is larger than %d bytes", path, maxSize)
	}
	return data, nil
}
