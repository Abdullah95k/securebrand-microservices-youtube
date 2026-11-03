// Package phase0 implements the Phase 0 measurement kit (plan §9): it runs
// the experiments on the server and keeps only what the report needs.
//
// Privacy: Phase 0 needs counts, not people. Records hold counts, sizes,
// timings and yes/no flags. Comment snapshots for the re-sweep keep comment
// IDs and a hash of the text, never the text or the author. Everything
// lives in the data folder on the server (never in the repo) and is kept
// (plan D26). The one exception is data from the official API, which
// YouTube's API terms let us keep for at most 30 days: PruneAPIData drops it.
package phase0

import (
	"bytes"
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

// APIDataMaxAge is how long data from the official API may be kept
// (YouTube API Services policies). Everything else is kept.
const APIDataMaxAge = 30 * 24 * time.Hour

// Store writes JSON-lines records and helper files under one folder.
type Store struct {
	dir   string
	mu    sync.Mutex
	skels map[string]int // skeletons saved per kind
}

// maxSkeletonsPerKind keeps a systematic parser problem from filling the
// disk: a few examples of each kind are enough to diagnose it.
const maxSkeletonsPerKind = 5

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
	data, err := os.ReadFile(filepath.Join(s.dir, kind+".jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Every complete record ends with a newline. Text after the last newline
	// is a record another process (a running crawl) is writing right now, so
	// it is skipped instead of failing the whole read.
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		data = data[:i+1]
	} else {
		data = nil
	}
	var out []T
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("phase0: %s.jsonl: %w", kind, err)
		}
		out = append(out, v)
	}
	return out, nil
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
	kind, _, _ := strings.Cut(name, "-")
	if s.skels == nil {
		s.skels = map[string]int{}
	}
	if s.skels[kind] >= maxSkeletonsPerKind {
		return
	}
	s.skels[kind]++
	dir := filepath.Join(s.dir, "skeletons")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name+".json"), sk, 0o600)
}

// rewrite replaces <kind>.jsonl with recs (atomically).
func rewrite[T any](s *Store, kind string, recs []T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var buf bytes.Buffer
	for _, r := range recs {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(append(data, '\n'))
	}
	p := filepath.Join(s.dir, kind+".jsonl")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// PruneAPIData removes what came from the official API more than
// APIDataMaxAge ago: the accuracy check's official numbers, the API's
// search results, and the titles of label-sample videos that only the API
// had found. The labels themselves (yes/no) and all scraped data stay. It
// returns how many records it removed or blanked.
func PruneAPIData(s *Store, now time.Time) (int, error) {
	cut := now.Add(-APIDataMaxAge)
	n := 0
	acc, err := ReadAll[AccuracyRecord](s, "accuracy")
	if err != nil {
		return 0, err
	}
	keptAcc := acc[:0:0]
	for _, r := range acc {
		if r.At.Before(cut) {
			n++
			continue
		}
		keptAcc = append(keptAcc, r)
	}
	if len(keptAcc) < len(acc) {
		if err := rewrite(s, "accuracy", keptAcc); err != nil {
			return n, err
		}
	}
	items, err := ReadAll[DiscoverItem](s, "discover_items")
	if err != nil {
		return n, err
	}
	keptItems := items[:0:0]
	dropped, still := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		if it.Engine == "api" && it.At.Before(cut) {
			dropped[it.VideoID] = true
			n++
			continue
		}
		still[it.VideoID] = true
		keptItems = append(keptItems, it)
	}
	if len(keptItems) < len(items) {
		if err := rewrite(s, "discover_items", keptItems); err != nil {
			return n, err
		}
	}
	sample, err := ReadAll[LabelItem](s, "label_sample")
	if err != nil {
		return n, err
	}
	blanked := false
	for i := range sample {
		if id := sample[i].VideoID; id != "" && dropped[id] && !still[id] {
			sample[i].VideoID, sample[i].Title = "", ""
			blanked = true
			n++
		}
	}
	if blanked {
		if err := rewrite(s, "label_sample", sample); err != nil {
			return n, err
		}
	}
	return n, nil
}
