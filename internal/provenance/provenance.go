// Package provenance remembers where a job's data came from, for the
// outbox's events (REQ-010 fix 12; the contract's Provenance): the job, the
// YouTube operations it read, where their raw answers are kept (fix 8) and
// the proxies' exit countries.
//
// The worker puts a Trail in each job's context; the InnerTube client
// records every answer it reads into it; each write takes what was read
// since the previous write, or, when nothing was read since (several
// writes from one answer: the videos of one tab page), the same as the
// previous write. A context without a Trail records nothing, so callers
// outside a job need no check.
package provenance

import (
	"context"
	"slices"
	"sync"
)

// Job identifies one run of a queued job.
type Job struct {
	ID   string // kind/entity/start in Unix milliseconds
	Kind string // the queue kind (channel, video, comments, sweep…)
}

// Source is what one write's data came from.
type Source struct {
	JobID         string   `json:"job_id,omitempty"`
	JobKind       string   `json:"job_kind,omitempty"`
	Operations    []string `json:"operations,omitempty"`     // browse, next, player, page…, in reading order, once each
	RawRefs       []string `json:"raw_refs,omitempty"`       // s3://… of each answer kept, in reading order
	ExitCountries []string `json:"exit_countries,omitempty"` // the proxies' country= tags, once each
}

// Trail collects one job's answers. Safe for concurrent use; a nil Trail
// ignores everything.
type Trail struct {
	job  Job
	mu   sync.Mutex
	read Source // since the last Take
	last Source // what the last Take returned
}

type trailKey struct{}

// With returns a context carrying a new Trail for job, and the Trail.
func With(ctx context.Context, job Job) (context.Context, *Trail) {
	t := &Trail{job: job}
	return context.WithValue(ctx, trailKey{}, t), t
}

// From returns the context's Trail, or nil.
func From(ctx context.Context) *Trail {
	t, _ := ctx.Value(trailKey{}).(*Trail)
	return t
}

// Record notes one answer read: its operation, where its raw copy is kept
// ("" when it isn't) and the exit's country ("" when the proxy list has
// no country= tag).
func (t *Trail) Record(operation, rawRef, country string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	addOnce(&t.read.Operations, operation)
	if rawRef != "" {
		t.read.RawRefs = append(t.read.RawRefs, rawRef)
	}
	addOnce(&t.read.ExitCountries, country)
}

func addOnce(list *[]string, v string) {
	if v != "" && !slices.Contains(*list, v) {
		*list = append(*list, v)
	}
}

// Take returns the job and what was read since the last Take, and starts
// afresh; when nothing was read since, what the last Take returned. A nil
// Trail returns an empty Source.
func (t *Trail) Take() Source {
	if t == nil {
		return Source{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.read.Operations) > 0 {
		t.last, t.read = t.read, Source{}
	}
	s := t.last
	s.JobID, s.JobKind = t.job.ID, t.job.Kind
	return s
}
