// Package metrics exposes numbers for Prometheus (plan §8.13) in its text
// format, without a client library: gauges are set, counters only grow.
// Labels never carry personal data or credentials: job kinds, endpoint
// classes, tiers and opaque proxy IDs only.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Registry holds metric families.
type Registry struct {
	mu   sync.Mutex
	fams map[string]*family
}

type family struct {
	help, typ string
	samples   map[string]float64 // rendered label set → value
}

// New returns an empty registry.
func New() *Registry { return &Registry{fams: map[string]*family{}} }

func labelString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(labels[k])
		parts[i] = fmt.Sprintf(`%s="%s"`, k, v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (r *Registry) fam(name, help, typ string) *family {
	f := r.fams[name]
	if f == nil {
		f = &family{help: help, typ: typ, samples: map[string]float64{}}
		r.fams[name] = f
	}
	return f
}

// Set sets a gauge.
func (r *Registry) Set(name, help string, labels map[string]string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fam(name, help, "gauge").samples[labelString(labels)] = v
}

// Add adds to a counter.
func (r *Registry) Add(name, help string, labels map[string]string, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fam(name, help, "counter").samples[labelString(labels)] += delta
}

// Reset drops a family's samples (before setting a fresh set of gauges,
// so label sets that disappeared do not linger).
func (r *Registry) Reset(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f := r.fams[name]; f != nil {
		f.samples = map[string]float64{}
	}
}

// WriteTo renders the text format.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.fams))
	for n := range r.fams {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		f := r.fams[n]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", n, f.help, n, f.typ)
		keys := make([]string, 0, len(f.samples))
		for k := range f.samples {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s%s %g\n", n, k, f.samples[k])
		}
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// Handler serves /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = r.WriteTo(w)
	})
}
