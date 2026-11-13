// Package fbdisc hands the Facebook links found on monitored channels'
// pages to the Facebook collector (fbs), which checks each page and may add
// it to its own schedule (the owner, D-065, 13 November: "activate Facebook
// discovery").
//
// The links come from what yts already reads at each channel refresh: the
// About panel's links and the channel description. No request is made to
// YouTube for this. Each link is sent once (by the link exactly as found);
// a channel whose links change has its new ones sent. Only listed channels
// are read, never commenters. At most Daily links go out per UTC day; while
// fbs answers 503 (its mentions off) or 429, yts waits an hour. Off unless
// the scheduler is given fbs's address and key.
package fbdisc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Path is fbs's candidates endpoint, below its API address.
const Path = "/v1/discovery/candidates"

// DefaultDaily is how many new links may go out a day to start (D-065).
const DefaultDaily = 200

// Pause is how long yts waits after fbs answers 503, 429, 401 or 403.
const Pause = time.Hour

// Results fbs answers with (its spec); "rejected" is yts's own, for a 4xx
// answer that a retry would not change.
var Results = []string{"queued", "on_list", "added", "public_waiting", "checked_recently", "ignored", "rejected"}

var (
	// A Facebook link: facebook.com and its subdomains (www, m, web), fb.com
	// and fb.me, with a path, with or without the scheme.
	linkRE = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-z0-9-]+\.)*(?:facebook\.com|fb\.com|fb\.me)/[^\s<>"'()\[\]{}|\\^]+`)
	// Punctuation that ends a sentence, not a link.
	trailing = ".,;:!?…"
)

// Find lists the Facebook links in a channel's About links and description,
// each exactly as found, once, in order.
func Find(links []string, description string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		for _, m := range linkRE.FindAllStringIndex(s, -1) {
			if m[0] > 0 && isHostChar(s[m[0]-1]) {
				continue // part of a longer host (notfacebook.com/…)
			}
			ref := strings.TrimRight(s[m[0]:m[1]], trailing)
			if strings.HasSuffix(strings.ToLower(ref), ".com/") || strings.HasSuffix(strings.ToLower(ref), ".me/") {
				continue // the bare site, no page
			}
			if !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
	}
	for _, l := range links {
		add(l)
	}
	add(description)
	return out
}

func isHostChar(c byte) bool {
	return c == '.' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// EvidenceURL is the channel page a link was found on.
func EvidenceURL(channelID string) string {
	return "https://www.youtube.com/channel/" + channelID + "/about"
}

// Sender finds new links and sends them to fbs.
type Sender struct {
	Pool   *pgxpool.Pool
	URL    string // fbs's API address
	Key    string // its bearer key: never logged
	Daily  int    // 0: DefaultDaily
	Client *http.Client
	Log    *slog.Logger
	Now    func() time.Time

	pausedUntil time.Time
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Stats says what one run did.
type Stats struct {
	Found, Sent int
	Paused      bool
}

// Run records the links of listed channels not seen before, then sends
// unsent ones, oldest first, within the day's limit.
func (s *Sender) Run(ctx context.Context) (Stats, error) {
	var st Stats
	n, err := s.scan(ctx)
	if err != nil {
		return st, err
	}
	st.Found = n
	now := s.now()
	if now.Before(s.pausedUntil) {
		st.Paused = true
		return st, nil
	}
	daily := s.Daily
	if daily <= 0 {
		daily = DefaultDaily
	}
	day := now.UTC().Truncate(24 * time.Hour)
	var sentToday int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM fb_candidates WHERE sent_at >= $1`, day).Scan(&sentToday); err != nil {
		return st, err
	}
	if sentToday >= daily {
		return st, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT ref, evidence_url FROM fb_candidates WHERE sent_at IS NULL
		ORDER BY first_seen, ref LIMIT $1`, daily-sentToday)
	if err != nil {
		return st, err
	}
	type cand struct{ ref, evidence string }
	cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		return c, r.Scan(&c.ref, &c.evidence)
	})
	if err != nil {
		return st, err
	}
	for _, c := range cands {
		result, pause, err := s.send(ctx, c.ref, c.evidence)
		if pause {
			s.pausedUntil = s.now().Add(Pause)
			st.Paused = true
			if s.Log != nil {
				s.Log.Warn("facebook discovery paused for an hour", "reason", err.Error())
			}
			return st, nil
		}
		if err != nil {
			// Kept unsent for the next run.
			if _, uerr := s.Pool.Exec(ctx, `UPDATE fb_candidates SET attempts = attempts + 1, last_error = $2 WHERE ref = $1`,
				c.ref, err.Error()); uerr != nil {
				return st, uerr
			}
			return st, err
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE fb_candidates SET sent_at = $2, result = $3, attempts = attempts + 1, last_error = NULL
			WHERE ref = $1`, c.ref, s.now(), result); err != nil {
			return st, err
		}
		st.Sent++
	}
	return st, nil
}

// scan records each listed channel's Facebook links not seen before.
func (s *Sender) scan(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, links, COALESCE(description, '') FROM channels WHERE status = 'listed'`)
	if err != nil {
		return 0, err
	}
	type found struct{ ref, channel string }
	var all []found
	for rows.Next() {
		var id, desc string
		var raw []byte
		if err := rows.Scan(&id, &raw, &desc); err != nil {
			rows.Close()
			return 0, err
		}
		var links []struct{ Title, URL string }
		_ = json.Unmarshal(raw, &links) // an unreadable list counts as empty
		urls := make([]string, 0, len(links))
		for _, l := range links {
			urls = append(urls, l.URL)
		}
		for _, ref := range Find(urls, desc) {
			all = append(all, found{ref, id})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	now := s.now()
	for _, f := range all {
		tag, err := s.Pool.Exec(ctx, `INSERT INTO fb_candidates (ref, channel_id, evidence_url, first_seen) VALUES ($1, $2, $3, $4)
			ON CONFLICT (ref) DO NOTHING`, f.ref, f.channel, EvidenceURL(f.channel), now)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// send posts one link. pause is true when fbs asks yts to wait (503, 429)
// or refuses the key (401, 403).
func (s *Sender) send(ctx context.Context, ref, evidence string) (result string, pause bool, err error) {
	body, _ := json.Marshal(map[string]string{"ref": ref, "source": "yts", "evidence_url": evidence})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+Path, bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("fbs: %w", redact(err, s.Key))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK:
		var a struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal(data, &a); err != nil || a.Result == "" {
			return "", false, fmt.Errorf("fbs: HTTP %d without a result", resp.StatusCode)
		}
		return a.Result, false, nil
	case resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return "", true, fmt.Errorf("fbs: HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return "rejected", false, nil
	}
	return "", false, fmt.Errorf("fbs: HTTP %d", resp.StatusCode)
}

// redact keeps the key out of an error (a client error can quote the request).
func redact(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[key]"))
}
