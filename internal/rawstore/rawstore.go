// Package rawstore keeps YouTube's raw responses (REQ-010 fix 8, standard
// E8; the owner's decision 3 of 6 November 2026).
//
// Every page and API answer the website client reads (images aside: they
// are media) is written, gzipped, with the parser version, to the raw
// bucket:
//
//   - yts/keep/: every failure (a block, an error page, an answer that
//     isn't JSON, a page the parsers couldn't read) and a random sample of
//     the rest (1% by default). Kept forever.
//   - yts/30d/: everything else. The platform's bucket rule may expire it
//     after 30 days; yts itself never deletes raw files.
//
// The object's reference (raw_ref) goes back with the response, into
// parse_failures, and later into each event's provenance (fix 12).
//
// Uploads run in the background: a slow bucket never slows collection.
// When the queue is full, a failure waits a little for room and a
// 30-day file is dropped (and counted).
package rawstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Prefixes in the raw bucket (the platform's convention).
const (
	KeepPrefix   = "yts/keep/"
	ExpirePrefix = "yts/30d/"
)

// Classes, as metrics and logs name them.
const (
	ClassKeep   = "keep"
	ClassExpire = "30d"
)

// Putter is the object store (media.Store satisfies it).
type Putter interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
}

// Record is one raw response, as stored: gzipped JSON, one object each.
type Record struct {
	V             int       `json:"v"` // format version
	At            time.Time `json:"at"`
	Collector     string    `json:"collector"` // "yts"
	ParserVersion string    `json:"parser_version"`
	Endpoint      string    `json:"endpoint"` // youtubei endpoint, or "page" for pages and feeds
	Method        string    `json:"method"`
	URL           string    `json:"url"` // no credentials: proxies are never in it
	Status        int       `json:"status"`
	Signal        string    `json:"signal,omitempty"`
	Proxy         string    `json:"proxy"` // the opaque proxy ID, never its address
	ContentType   string    `json:"content_type,omitempty"`
	Kept          string    `json:"kept,omitempty"` // why it is in yts/keep/: the failure, or "sample"
	Body          string    `json:"body,omitempty"`
	BodyBase64    []byte    `json:"body_base64,omitempty"` // only when the body isn't UTF-8
}

// Options configure a Store.
type Options struct {
	Sample        float64 // share of ordinary responses kept forever (default 0.01)
	SkipExpiring  bool    // write only failures and the sample (not the 30-day rest)
	RefBase       string  // what raw_ref starts with, for example "s3://bucket/"
	ParserVersion string
	Workers       int           // parallel uploads (default 2)
	Queue         int           // uploads waiting (default 256; about 50 KB each)
	RetryWait     time.Duration // before the second and third try (default 1 s, then 2 s)
	Log           *slog.Logger
	// OnResult counts each object: class, then "stored", "failed" or
	// "dropped", and its size. nil = none.
	OnResult func(class, result string, bytes int)
}

type upload struct {
	key, class string
	data       []byte
}

// Store captures raw responses. A nil *Store captures nothing.
type Store struct {
	put  Putter
	opt  Options
	q    chan upload
	mu   sync.Mutex
	rng  *mrand.Rand
	wg   sync.WaitGroup
	once sync.Once
}

// New creates a Store; Start runs its uploads.
func New(p Putter, opt Options) *Store {
	if opt.Sample <= 0 {
		opt.Sample = 0.01
	}
	if opt.Sample > 1 {
		opt.Sample = 1
	}
	if opt.Workers <= 0 {
		opt.Workers = 2
	}
	if opt.Queue <= 0 {
		opt.Queue = 256
	}
	if opt.RetryWait <= 0 {
		opt.RetryWait = time.Second
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	var seed [16]byte
	_, _ = rand.Read(seed[:])
	return &Store{put: p, opt: opt, q: make(chan upload, opt.Queue),
		rng: mrand.New(mrand.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:])))}
}

// Start runs the uploads until stop is called; stop then waits up to
// flush for the queue to empty.
func (s *Store) Start(flush time.Duration) (stop func()) {
	if s == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < s.opt.Workers; i++ {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for u := range s.q {
				s.store(ctx, u)
			}
		}()
	}
	return func() {
		s.once.Do(func() {
			close(s.q)
			done := make(chan struct{})
			go func() { s.wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(flush):
				s.opt.Log.Warn("raw responses not uploaded at shutdown", "waiting", len(s.q))
			}
			cancel()
		})
	}
}

func (s *Store) store(ctx context.Context, u upload) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(attempt) * s.opt.RetryWait):
			}
		}
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = s.put.Put(pctx, u.key, "application/gzip", u.data)
		cancel()
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		s.opt.Log.Warn("storing a raw response failed", "class", u.class, "err", err)
		s.result(u.class, "failed", len(u.data))
		return
	}
	s.result(u.class, "stored", len(u.data))
}

func (s *Store) result(class, result string, n int) {
	if s.opt.OnResult != nil {
		s.opt.OnResult(class, result, n)
	}
}

// Capture queues one response and returns its raw_ref. failure is why it
// must be kept ("" for an ordinary answer, which is sampled). It returns ""
// when nothing will be stored (a nil Store, or the 30-day rest is off).
func (s *Store) Capture(ctx context.Context, rec Record, body []byte, failure string) string {
	if s == nil {
		return ""
	}
	class := ClassExpire
	switch {
	case failure != "":
		class, rec.Kept = ClassKeep, "failure: "+limit(failure, 200)
	case s.sampled():
		class, rec.Kept = ClassKeep, "sample"
	case s.opt.SkipExpiring:
		return ""
	}
	data, err := s.encode(rec, body)
	if err != nil {
		s.opt.Log.Warn("encoding a raw response failed", "err", err)
		return ""
	}
	key := Key(class, rec.At, rec.Endpoint)
	ref := s.opt.RefBase + key
	if !s.enqueue(upload{key: key, class: class, data: data}) {
		return ""
	}
	JobFrom(ctx).add(ref, rec, body, class)
	return ref
}

// Keep stores responses again under yts/keep/, with why: for answers that
// looked fine to the client but that the parsers couldn't read. It returns
// their raw_refs.
func (s *Store) Keep(ctx context.Context, recent []Captured, why string) []string {
	if s == nil {
		return nil
	}
	var refs []string
	for _, c := range recent {
		if c.Class == ClassKeep {
			refs = append(refs, c.Ref) // already kept forever
			continue
		}
		rec := c.Record
		rec.Kept = "failure: " + limit(why, 200)
		data, err := s.encode(rec, c.body)
		if err != nil {
			continue
		}
		key := Key(ClassKeep, rec.At, rec.Endpoint)
		if s.enqueue(upload{key: key, class: ClassKeep, data: data}) {
			refs = append(refs, s.opt.RefBase+key)
		}
	}
	return refs
}

func (s *Store) enqueue(u upload) bool {
	select {
	case s.q <- u:
		return true
	default:
	}
	if u.class == ClassKeep {
		// A failure is worth a short wait.
		t := time.NewTimer(2 * time.Second)
		defer t.Stop()
		select {
		case s.q <- u:
			return true
		case <-t.C:
		}
	}
	s.result(u.class, "dropped", len(u.data))
	return false
}

func (s *Store) sampled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Float64() < s.opt.Sample
}

func (s *Store) encode(rec Record, body []byte) ([]byte, error) {
	rec.V, rec.Collector, rec.ParserVersion = 1, "yts", s.opt.ParserVersion
	if utf8.Valid(body) {
		rec.Body = string(body)
	} else {
		rec.BodyBase64 = body
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Key names an object: the class prefix, the UTC date, the endpoint, then
// the time and a random part, so a listing is in time order.
func Key(class string, at time.Time, endpoint string) string {
	prefix := ExpirePrefix
	if class == ClassKeep {
		prefix = KeepPrefix
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	at = at.UTC()
	return fmt.Sprintf("%s%s/%s/%s-%s.json.gz", prefix, at.Format("2006/01/02"), safe(endpoint),
		at.Format("150405.000000"), hex.EncodeToString(b[:]))
}

// safe keeps an endpoint name usable in a key ("navigation/resolve_url").
func safe(s string) string {
	if s == "" {
		return "page"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.ToLower(s))
}

func limit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
