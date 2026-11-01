// Package phase0 implements the Phase 0 measurement kit (plan §9): it runs
// the experiments on the server and keeps only what the report needs.
//
// Privacy: Phase 0 needs counts, not people. Records hold counts, sizes,
// timings and yes/no flags. Comment snapshots for the re-sweep keep comment
// IDs and a hash of the text, never the text or the author. Everything
// lives in the data folder on the server (never in the repo) and is deleted
// after 14 days.
package phase0

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/skeleton"
)

// Retention is how long Phase 0 data is kept on the server.
const Retention = 14 * 24 * time.Hour

// Store writes JSON-lines records and helper files under one folder.
type Store struct {
	dir string
	mu  sync.Mutex
}

// OpenStore creates the folder (owner-only permissions) if needed.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("phase0: data folder not set (YT_DATA_DIR)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("phase0: creating data folder: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the data folder.
func (s *Store) Dir() string { return s.dir }

// Append adds one record to <kind>.jsonl.
func (s *Store) Append(kind string, rec any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dir, kind+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadAll decodes every record of <kind>.jsonl.
func ReadAll[T any](s *Store, kind string) ([]T, error) {
	f, err := os.Open(filepath.Join(s.dir, kind+".jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("phase0: %s.jsonl: %w", kind, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// WriteJSON writes a JSON file under the data folder (atomically).
func (s *Store) WriteJSON(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ReadJSON reads a JSON file written by WriteJSON. ok is false if missing.
func (s *Store) ReadJSON(name string, v any) (ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, v)
}

// List returns file names under a sub-folder, sorted.
func (s *Store) List(sub string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, sub))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// SaveSkeleton stores the structure (no values) of a response a parser could
// not read, for review. Raw responses are never stored.
func (s *Store) SaveSkeleton(name string, body []byte) {
	sk, err := skeleton.Bytes(body)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.dir, "skeletons")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name+".json"), sk, 0o600)
}

// Cleanup deletes files older than maxAge. It returns how many it removed.
func (s *Store) Cleanup(now time.Time, maxAge time.Duration) (int, error) {
	removed := 0
	err := filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) > maxAge {
			if err := os.Remove(p); err == nil {
				removed++
			}
		}
		return nil
	})
	return removed, err
}
