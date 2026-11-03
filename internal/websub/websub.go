// Package websub receives YouTube's push notifications (WebSub, plan §8.5):
// the hub POSTs to our public HTTPS endpoint as soon as a listed channel
// uploads, and the video is fetched at once. It needs a public subdomain
// (D23), so it stays off until a callback URL is configured; feeds and tab
// checks find new uploads meanwhile.
//
// Each channel gets its own secret, derived from one server secret, and
// every notification's signature is checked before it is used.
package websub

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/detect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/feeds"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
)

// DefaultHub is YouTube's hub.
const DefaultHub = "https://pubsubhubbub.appspot.com/subscribe"

// TopicURL is the feed a channel's notifications are about.
func TopicURL(channelID string) string {
	return "https://www.youtube.com/xml/feeds/videos.xml?channel_id=" + url.QueryEscape(channelID)
}

// Config is shared by the subscriber and the receiver.
type Config struct {
	Callback string // public HTTPS URL of the receiver, e.g. https://push.example.com/websub
	Secret   []byte // server secret (from a file); never sent as is
	Hub      string // default DefaultHub
	Lease    time.Duration
	HTTP     *http.Client
}

// Enabled reports whether push is configured.
func (c Config) Enabled() bool { return c.Callback != "" && len(c.Secret) >= 32 }

// channelSecret is the secret the hub signs one channel's notifications with.
func (c Config) channelSecret(channelID string) string {
	m := hmac.New(sha256.New, c.Secret)
	m.Write([]byte("websub\x00" + channelID))
	return hex.EncodeToString(m.Sum(nil))
}

func (c Config) callbackFor(channelID string) string {
	sep := "?"
	if strings.Contains(c.Callback, "?") {
		sep = "&"
	}
	return c.Callback + sep + "channel=" + url.QueryEscape(channelID)
}

// Subscriber (re)subscribes listed channels; it runs as the "websub" job.
type Subscriber struct {
	Pool *pgxpool.Pool
	Cfg  Config
	Log  *slog.Logger
	Now  func() time.Time
}

func (s *Subscriber) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Subscriber) lease() time.Duration {
	if s.Cfg.Lease <= 0 {
		return 10 * 24 * time.Hour
	}
	return s.Cfg.Lease
}

// Handlers maps the subscription job kind to its handler.
func (s *Subscriber) Handlers() map[string]worker.Handler {
	return map[string]worker.Handler{queue.KindWebSub: s.subscribe}
}

// Ensure gives every listed channel a subscription job (when push is on).
func (s *Subscriber) Ensure(ctx context.Context) (int, error) {
	if !s.Cfg.Enabled() {
		return 0, nil
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO schedule (kind, entity_id, channel_id, priority, due_at)
		SELECT $1, id, id, $2, now() FROM channels WHERE status = 'listed'
		ON CONFLICT (kind, entity_id) DO NOTHING`, queue.KindWebSub, queue.P2)
	return int(tag.RowsAffected()), err
}

func (s *Subscriber) subscribe(ctx context.Context, j queue.Job) (time.Time, error) {
	var status string
	err := s.Pool.QueryRow(ctx, `SELECT status FROM channels WHERE id = $1`, j.EntityID).Scan(&status)
	mode := "subscribe"
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "listed" {
		mode = "unsubscribe"
	} else if err != nil {
		return time.Time{}, err
	}
	if !s.Cfg.Enabled() {
		return time.Time{}, nil
	}
	form := url.Values{
		"hub.callback":      {s.Cfg.callbackFor(j.EntityID)},
		"hub.topic":         {TopicURL(j.EntityID)},
		"hub.mode":          {mode},
		"hub.verify":        {"async"},
		"hub.lease_seconds": {strconv.Itoa(int(s.lease() / time.Second))},
		"hub.secret":        {s.Cfg.channelSecret(j.EntityID)},
	}
	hub := s.Cfg.Hub
	if hub == "" {
		hub = DefaultHub
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub, strings.NewReader(form.Encode()))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := s.Cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("websub hub: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("websub hub: HTTP %d", resp.StatusCode)
	}
	if mode == "unsubscribe" {
		return time.Time{}, nil
	}
	return s.now().Add(s.lease() - 24*time.Hour), nil // renew a day early
}

// Receiver is the HTTP endpoint the hub calls.
type Receiver struct {
	Pool    *pgxpool.Pool
	Cfg     Config
	RSS     *detect.RSS // shares the feed's entry handling
	Log     *slog.Logger
	MaxBody int64
}

func (rc *Receiver) listed(ctx context.Context, id string) (kind string, ok bool) {
	var status string
	if err := rc.Pool.QueryRow(ctx, `SELECT kind, status FROM channels WHERE id = $1`, id).Scan(&kind, &status); err != nil {
		return "", false
	}
	return kind, status == "listed"
}

// ServeHTTP answers the hub's verification (GET) and notifications (POST).
func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		mode, topic, challenge := q.Get("hub.mode"), q.Get("hub.topic"), q.Get("hub.challenge")
		_, listed := rc.listed(r.Context(), channel)
		ok := channel != "" && topic == TopicURL(channel) && challenge != "" &&
			(mode == "subscribe" && listed || mode == "unsubscribe" && !listed)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, challenge)
	case http.MethodPost:
		max := rc.MaxBody
		if max <= 0 {
			max = 1 << 20
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, max))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// The hub expects a 2xx even for notifications we drop.
		w.WriteHeader(http.StatusNoContent)
		if !rc.validSignature(channel, r.Header.Get("X-Hub-Signature"), body) {
			rc.Log.Warn("websub notification with a bad signature dropped")
			return
		}
		kind, listed := rc.listed(r.Context(), channel)
		if !listed {
			return
		}
		feed, err := feeds.Parse(body)
		if err != nil {
			rc.Log.Warn("websub notification not understood", "err", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, e := range feed.Entries {
			if e.VideoID == "" || e.ChannelID != "" && e.ChannelID != channel {
				continue
			}
			if _, err := rc.RSS.Entry(ctx, channel, kind, e, "websub"); err != nil {
				rc.Log.Warn("websub entry failed", "err", err)
			}
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (rc *Receiver) validSignature(channel, header string, body []byte) bool {
	algo, sig, ok := strings.Cut(header, "=")
	if !ok || algo != "sha1" || channel == "" {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	m := hmac.New(sha1.New, []byte(rc.Cfg.channelSecret(channel)))
	m.Write(body)
	return hmac.Equal(m.Sum(nil), want)
}
