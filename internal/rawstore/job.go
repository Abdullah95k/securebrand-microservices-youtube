package rawstore

import (
	"context"
	"sync"
)

// recentPerJob is how many of a job's latest responses it remembers, so a
// parse failure can keep the pages it was read from. A page the parsers
// reject is one of the last few the job read.
const recentPerJob = 4

// Captured is one response a job read: its raw_ref and class, and what
// Keep needs to store it again.
type Captured struct {
	Ref    string
	Class  string
	Record Record
	body   []byte
}

// Job remembers a job's latest captured responses. A nil *Job remembers
// nothing.
type Job struct {
	mu     sync.Mutex
	recent []Captured
}

type jobKey struct{}

// WithJob returns a context that remembers the job's responses.
func WithJob(ctx context.Context) (context.Context, *Job) {
	j := &Job{}
	return context.WithValue(ctx, jobKey{}, j), j
}

// JobFrom returns the context's Job, or nil.
func JobFrom(ctx context.Context) *Job {
	j, _ := ctx.Value(jobKey{}).(*Job)
	return j
}

func (j *Job) add(ref string, rec Record, body []byte, class string) {
	if j == nil {
		return
	}
	if class == ClassKeep {
		body = nil // already kept forever: Keep won't store it again
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.recent = append(j.recent, Captured{Ref: ref, Class: class, Record: rec, body: body})
	if len(j.recent) > recentPerJob {
		j.recent = append(j.recent[:0:0], j.recent[len(j.recent)-recentPerJob:]...)
	}
}

// Recent returns the job's latest captured responses, newest last.
func (j *Job) Recent() []Captured {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Captured(nil), j.recent...)
}

// KeepRecent stores the job's latest responses under yts/keep/ with why,
// and returns their raw_refs (for parse_failures.raw_ref).
func (s *Store) KeepRecent(ctx context.Context, why string) []string {
	return s.Keep(ctx, JobFrom(ctx).Recent(), why)
}
