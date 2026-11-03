// Package webhook sends events to other systems (plan §8.2 "webhooks", D20):
// a video or community post that matches the keywords, a channel put on
// the list, and erasures (so the receiving side can delete what it got).
//
// Events are recorded in an outbox table by database triggers; the
// dispatcher creates one delivery per event and endpoint and POSTs it as
// JSON, signed with HMAC-SHA256. A failed delivery is retried with backoff
// (30 s, 1 min, 2 min … at most 6 h between tries) and after MaxAttempts
// it is marked dead (the dead-letter queue: "ytcollect webhooks retry").
//
// Each request carries:
//
//	X-SecureBrand-Event:     the event type
//	X-SecureBrand-Delivery:  the event ID (the same on every retry: use it to drop duplicates)
//	X-SecureBrand-Timestamp: Unix seconds
//	X-SecureBrand-Signature: v1=<hex HMAC-SHA256(secret, timestamp + "." + body)>
//
// The receiver checks the signature and that the timestamp is within 5
// minutes, and answers 2xx. Endpoint URLs can hold tokens, so they are
// never logged; secrets are read from files.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
)

// Event types.
const (
	VideoRelevant = "video.relevant"
	PostRelevant  = "post.relevant"
	ChannelListed = "channel.listed"
	Erasure       = "erasure"
	Ping          = "ping" // "ytcollect webhooks test" only
)

// Types lists the event types an endpoint can subscribe to.
var Types = []string{VideoRelevant, PostRelevant, ChannelListed, Erasure}

// DefaultConfigFile is where the endpoints are configured.
const DefaultConfigFile = "/etc/securebrand-youtube/webhooks.json"

// MaxAttempts: a delivery that failed this many times is dead.
const MaxAttempts = 15

// lookback: events this recent are checked for missing deliveries.
const lookback = 7 * 24 * time.Hour

// Endpoint is one receiver, for example
//
//	{"name": "fbs", "url": "https://hooks.example.com/youtube",
//	 "secret_file": "/etc/securebrand-youtube/webhook_fbs_secret",
//	 "events": ["video.relevant", "post.relevant", "channel.listed", "erasure"]}
type Endpoint struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	SecretFile string   `json:"secret_file"`
	Events     []string `json:"events"`
	secret     []byte
}

// Config is the endpoints file.
type Config struct {
	Endpoints []Endpoint `json:"endpoints"`
}

// ErrNotConfigured: there is no endpoints file (webhooks are off).
var ErrNotConfigured = errors.New("webhook: no endpoints configured")

var validName = func(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// CheckURL accepts https URLs (and plain http to this machine, for tests).
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("webhook: the endpoint URL is not valid")
	}
	host := u.Hostname()
	if u.Scheme == "https" || u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return errors.New("webhook: the endpoint URL must use https")
}

// Load reads the endpoints file (DefaultConfigFile when path is empty) and
// each endpoint's secret.
func Load(path string) (Config, error) {
	if path == "" {
		path = DefaultConfigFile
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotConfigured
	}
	if err != nil {
		return Config{}, fmt.Errorf("webhook: endpoints file: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("webhook: %s is not valid JSON", path)
	}
	seen := map[string]bool{}
	for i := range c.Endpoints {
		e := &c.Endpoints[i]
		if !validName(e.Name) || seen[e.Name] {
			return Config{}, fmt.Errorf("webhook: endpoint name %q: lowercase letters, digits, - and _, unique", e.Name)
		}
		seen[e.Name] = true
		if err := CheckURL(e.URL); err != nil {
			return Config{}, fmt.Errorf("%w (endpoint %s)", err, e.Name)
		}
		if len(e.Events) == 0 {
			return Config{}, fmt.Errorf("webhook: endpoint %s subscribes to no events (%s)", e.Name, strings.Join(Types, ", "))
		}
		for _, t := range e.Events {
			if !known(t) {
				return Config{}, fmt.Errorf("webhook: endpoint %s: unknown event %q (%s)", e.Name, t, strings.Join(Types, ", "))
			}
		}
		s, err := secrets.ReadFile(e.SecretFile)
		if err != nil {
			return Config{}, fmt.Errorf("webhook: endpoint %s secret: %w", e.Name, err)
		}
		if len(s) < 32 {
			return Config{}, fmt.Errorf("webhook: endpoint %s secret: at least 32 characters (openssl rand -hex 32)", e.Name)
		}
		e.secret = []byte(s)
	}
	return c, nil
}

// WithSecret sets an endpoint's secret (tests).
func (e Endpoint) WithSecret(s string) Endpoint {
	e.secret = []byte(s)
	return e
}

func known(t string) bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

// Sign is the X-SecureBrand-Signature value for a body sent at ts.
func Sign(secret []byte, ts int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strconv.FormatInt(ts, 10)))
	m.Write([]byte("."))
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a request's signature and age (for receivers written in
// Go, and the tests).
func Verify(secret []byte, header http.Header, body []byte, now time.Time) error {
	ts, err := strconv.ParseInt(header.Get("X-SecureBrand-Timestamp"), 10, 64)
	if err != nil {
		return errors.New("webhook: no timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return errors.New("webhook: timestamp too old or in the future")
	}
	if !hmac.Equal([]byte(header.Get("X-SecureBrand-Signature")), []byte(Sign(secret, ts, body))) {
		return errors.New("webhook: bad signature")
	}
	return nil
}

// Emit records an event (the triggers record the others).
func Emit(ctx context.Context, q db.Querier, typ, entityID, channelID string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO webhook_events (type, entity_id, channel_id, payload) VALUES ($1, $2, NULLIF($3, ''), $4)`,
		typ, entityID, channelID, data)
	return err
}

// Backoff is the wait after the n-th failed attempt.
func Backoff(n int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < n && d < 6*time.Hour; i++ {
		d *= 2
	}
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	return d
}

// Dispatcher creates and sends deliveries.
type Dispatcher struct {
	Pool *pgxpool.Pool
	Cfg  Config
	HTTP *http.Client
	Log  *slog.Logger
}

func (d *Dispatcher) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // a redirect is a failed delivery, never followed
	}}
}

func (d *Dispatcher) endpoint(name string) (Endpoint, bool) {
	for _, e := range d.Cfg.Endpoints {
		if e.Name == name {
			return e, true
		}
	}
	return Endpoint{}, false
}

// Enqueue creates the deliveries that are missing: for every endpoint,
// each recent event of a type it subscribes to, from after the endpoint
// was added. It returns how many it created.
func (d *Dispatcher) Enqueue(ctx context.Context) (int64, error) {
	var total int64
	for _, e := range d.Cfg.Endpoints {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO webhook_endpoints (name, first_event)
			SELECT $1, COALESCE(max(id), 0) FROM webhook_events ON CONFLICT (name) DO NOTHING`, e.Name); err != nil {
			return total, err
		}
		tag, err := d.Pool.Exec(ctx, `INSERT INTO webhook_deliveries (event_id, endpoint)
			SELECT ev.id, $1 FROM webhook_events ev, webhook_endpoints ep
			WHERE ep.name = $1 AND ev.id > ep.first_event AND ev.type = ANY($2) AND ev.created_at > now() - make_interval(secs => $3)
			AND NOT EXISTS (SELECT 1 FROM webhook_deliveries dl WHERE dl.event_id = ev.id AND dl.endpoint = $1)
			ON CONFLICT DO NOTHING`, e.Name, e.Events, lookback.Seconds())
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

type due struct {
	eventID  int64
	endpoint string
	attempts int
	typ      string
	payload  json.RawMessage
	created  time.Time
}

// Body is what an endpoint receives.
type Body struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// post sends one body; it returns the HTTP status (0: no answer).
func (d *Dispatcher) post(ctx context.Context, e Endpoint, id int64, typ string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("webhook: bad endpoint URL")
	}
	ts := time.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "securebrand-youtube-webhooks/1")
	req.Header.Set("X-SecureBrand-Event", typ)
	req.Header.Set("X-SecureBrand-Delivery", strconv.FormatInt(id, 10))
	req.Header.Set("X-SecureBrand-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-SecureBrand-Signature", Sign(e.secret, ts, body))
	resp, err := d.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL may hold a token: never in errors or logs
		}
		return 0, fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Deliver sends up to n due deliveries and returns how many succeeded.
func (d *Dispatcher) Deliver(ctx context.Context, n int) (int, error) {
	rows, err := d.Pool.Query(ctx, `UPDATE webhook_deliveries dl SET lease_until = now() + interval '2 minutes'
		FROM webhook_events ev
		WHERE ev.id = dl.event_id AND (dl.event_id, dl.endpoint) IN (
			SELECT event_id, endpoint FROM webhook_deliveries
			WHERE status = 'pending' AND next_at <= now() AND (lease_until IS NULL OR lease_until < now())
			ORDER BY next_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING dl.event_id, dl.endpoint, dl.attempts, ev.type, ev.payload, ev.created_at`, n)
	if err != nil {
		return 0, err
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var x due
		err := r.Scan(&x.eventID, &x.endpoint, &x.attempts, &x.typ, &x.payload, &x.created)
		return x, err
	})
	if err != nil {
		return 0, err
	}
	ok := 0
	for _, x := range batch {
		e, known := d.endpoint(x.endpoint)
		if !known {
			// The endpoint was removed from the file: its deliveries wait
			// (they go out if it comes back; "webhooks list" shows them).
			if _, err := d.Pool.Exec(ctx, `UPDATE webhook_deliveries SET lease_until = NULL, next_at = now() + interval '1 hour'
				WHERE event_id = $1 AND endpoint = $2`, x.eventID, x.endpoint); err != nil {
				return ok, err
			}
			continue
		}
		body, _ := json.Marshal(Body{ID: x.eventID, Type: x.typ, CreatedAt: x.created.UTC(), Data: x.payload})
		status, perr := d.post(ctx, e, x.eventID, x.typ, body)
		var st *int
		if status != 0 {
			st = &status
		}
		if perr == nil {
			ok++
			if _, err := d.Pool.Exec(ctx, `UPDATE webhook_deliveries SET status = 'delivered', delivered_at = now(), lease_until = NULL,
				attempts = attempts + 1, last_status = $3, last_error = NULL WHERE event_id = $1 AND endpoint = $2`,
				x.eventID, x.endpoint, st); err != nil {
				return ok, err
			}
			continue
		}
		attempts := x.attempts + 1
		status2 := "pending"
		if attempts >= MaxAttempts {
			status2 = "dead"
			d.Log.Warn("webhook delivery gave up", "endpoint", x.endpoint, "event", x.eventID, "type", x.typ, "attempts", attempts)
		}
		if _, err := d.Pool.Exec(ctx, `UPDATE webhook_deliveries SET status = $3, attempts = $4, lease_until = NULL,
			next_at = now() + make_interval(secs => $5), last_status = $6, last_error = left($7, 300)
			WHERE event_id = $1 AND endpoint = $2`,
			x.eventID, x.endpoint, status2, attempts, Backoff(attempts).Seconds(), st, perr.Error()); err != nil {
			return ok, err
		}
	}
	return ok, d.redactErasures(ctx)
}

// redactErasures removes the erased item's ID from erasure events once
// every endpoint has it (or gave up), and from those no endpoint takes: an
// erasure keeps no raw ID longer than it must.
func (d *Dispatcher) redactErasures(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, `UPDATE webhook_events ev SET payload = jsonb_build_object('kind', ev.payload->'kind', 'redacted', true)
		WHERE ev.type = 'erasure' AND NOT (ev.payload ? 'redacted') AND ev.created_at < now() - interval '10 minutes'
		AND NOT EXISTS (SELECT 1 FROM webhook_deliveries dl WHERE dl.event_id = ev.id AND dl.status = 'pending')`)
	return err
}

// Run creates and sends deliveries every interval until ctx ends.
func (d *Dispatcher) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := d.Enqueue(ctx); err != nil && ctx.Err() == nil {
			d.Log.Warn("creating webhook deliveries failed", "err", err)
		}
		for {
			n, err := d.Deliver(ctx, 100)
			if err != nil && ctx.Err() == nil {
				d.Log.Warn("sending webhooks failed", "err", err)
			}
			if n < 100 || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Test sends a ping to one endpoint now (not through the outbox) and
// returns the HTTP status.
func (d *Dispatcher) Test(ctx context.Context, name string) (int, error) {
	e, ok := d.endpoint(name)
	if !ok {
		return 0, fmt.Errorf("webhook: no endpoint named %q", name)
	}
	body, _ := json.Marshal(Body{ID: 0, Type: Ping, CreatedAt: time.Now().UTC(), Data: json.RawMessage(`{}`)})
	return d.post(ctx, e, 0, Ping, body)
}

// Retry puts an endpoint's dead deliveries back in the queue ("" = all).
func Retry(ctx context.Context, q db.Querier, endpoint string) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE webhook_deliveries SET status = 'pending', attempts = 0, next_at = now(), last_error = NULL
		WHERE status = 'dead' AND ($1 = '' OR endpoint = $1)`, endpoint)
	return tag.RowsAffected(), err
}

// Count is deliveries per endpoint and status.
type Count struct {
	Endpoint, Status string
	N                int64
	Oldest           time.Time
}

// Counts lists deliveries per endpoint and status, with the oldest event.
func Counts(ctx context.Context, q db.Querier) ([]Count, error) {
	rows, err := q.Query(ctx, `SELECT dl.endpoint, dl.status, count(*), min(ev.created_at) FROM webhook_deliveries dl
		JOIN webhook_events ev ON ev.id = dl.event_id GROUP BY 1, 2 ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Count, error) {
		var c Count
		err := r.Scan(&c.Endpoint, &c.Status, &c.N, &c.Oldest)
		return c, err
	})
}
