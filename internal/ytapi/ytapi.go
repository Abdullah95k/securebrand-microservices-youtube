// Package ytapi is a minimal client for the official YouTube Data API v3,
// used only for keyword search (D4) and the one-time Phase 0 accuracy check
// (D13).
//
// The API key is sent in the X-Goog-Api-Key header, never in the URL, so it
// can't leak through logged URLs (plan §7.3). Every call is charged to a
// quota ledger before it is sent; the ledger refuses calls past the daily
// ceiling, which resets at midnight US Pacific time (plan §4.2).
package ytapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Unit costs of the methods we use.
const (
	CostSearch          = 100
	CostVideosList      = 1
	CostVideoCategories = 1
)

// DefaultDailyQuota is the default allocation of a Google Cloud project.
const DefaultDailyQuota = 10000

// ErrQuota means the call would exceed today's quota ceiling.
var ErrQuota = errors.New("ytapi: daily quota ceiling reached (resets at midnight US Pacific time)")

// Client calls the Data API.
type Client struct {
	key     string
	base    string
	http    *http.Client
	ledger  *Ledger
	nowFunc func() time.Time
}

// New creates a client. base is "" for the real API.
func New(apiKey, base string, ledger *Ledger) *Client {
	if base == "" {
		base = "https://www.googleapis.com/youtube/v3"
	}
	return &Client{
		key:     apiKey,
		base:    strings.TrimRight(base, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		ledger:  ledger,
		nowFunc: time.Now,
	}
}

// APIError is an error answer from the API (key and URL are never included).
type APIError struct {
	Status int
	Reason string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ytapi: HTTP %d (%s)", e.Status, e.Reason)
}

func (c *Client) get(ctx context.Context, method string, cost int, params url.Values, out any) error {
	if err := c.ledger.Charge(c.nowFunc(), method, cost); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/"+method+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Goog-Api-Key", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		// Report the underlying network error only (the URL holds no key,
		// but there is no need to log it).
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("ytapi: %s request failed: %w", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Errors []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		reason := "unknown"
		if len(e.Error.Errors) > 0 {
			reason = e.Error.Errors[0].Reason
		}
		return &APIError{Status: resp.StatusCode, Reason: reason}
	}
	return json.Unmarshal(body, out)
}

// SearchOptions narrows a search.
type SearchOptions struct {
	PublishedAfter    time.Time
	RegionCode        string // "IQ"
	RelevanceLanguage string // "ar", "ku", "en"
	PageToken         string
	MaxResults        int // up to 50
}

// SearchHit is one video found by search.
type SearchHit struct {
	VideoID      string
	ChannelID    string
	ChannelTitle string
	Title        string
	PublishedAt  time.Time
}

// SearchResult is one page of search results.
type SearchResult struct {
	Hits          []SearchHit
	NextPageToken string
	TotalResults  int
}

// Search finds recent videos matching q (OR with "|", exclude with "-"),
// newest first. Costs 100 units.
func (c *Client) Search(ctx context.Context, q string, o SearchOptions) (*SearchResult, error) {
	p := url.Values{}
	p.Set("part", "snippet")
	p.Set("type", "video")
	p.Set("order", "date")
	p.Set("q", q)
	if o.MaxResults <= 0 || o.MaxResults > 50 {
		o.MaxResults = 50
	}
	p.Set("maxResults", strconv.Itoa(o.MaxResults))
	if !o.PublishedAfter.IsZero() {
		p.Set("publishedAfter", o.PublishedAfter.UTC().Format(time.RFC3339))
	}
	if o.RegionCode != "" {
		p.Set("regionCode", o.RegionCode)
	}
	if o.RelevanceLanguage != "" {
		p.Set("relevanceLanguage", o.RelevanceLanguage)
	}
	if o.PageToken != "" {
		p.Set("pageToken", o.PageToken)
	}
	var raw struct {
		NextPageToken string `json:"nextPageToken"`
		PageInfo      struct {
			TotalResults int `json:"totalResults"`
		} `json:"pageInfo"`
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
			Snippet struct {
				ChannelID    string    `json:"channelId"`
				ChannelTitle string    `json:"channelTitle"`
				Title        string    `json:"title"`
				PublishedAt  time.Time `json:"publishedAt"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := c.get(ctx, "search", CostSearch, p, &raw); err != nil {
		return nil, err
	}
	res := &SearchResult{NextPageToken: raw.NextPageToken, TotalResults: raw.PageInfo.TotalResults}
	for _, it := range raw.Items {
		res.Hits = append(res.Hits, SearchHit{
			VideoID: it.ID.VideoID, ChannelID: it.Snippet.ChannelID, ChannelTitle: it.Snippet.ChannelTitle,
			Title: it.Snippet.Title, PublishedAt: it.Snippet.PublishedAt,
		})
	}
	return res, nil
}

// VideoStats are official numbers for the Phase 0 accuracy check (E4).
type VideoStats struct {
	VideoID       string
	Views         int64
	Likes         int64
	Comments      int64
	HasLikes      bool
	HasComments   bool
	PublishedAt   time.Time
	DurationISO   string
	CategoryID    string
	TagCount      int
	ChannelID     string
	LiveBroadcast string
}

// VideosList fetches up to 50 videos in one call (1 unit).
func (c *Client) VideosList(ctx context.Context, ids []string) ([]VideoStats, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 50 {
		return nil, errors.New("ytapi: at most 50 IDs per videos.list call")
	}
	p := url.Values{}
	p.Set("part", "snippet,statistics,contentDetails")
	p.Set("id", strings.Join(ids, ","))
	p.Set("maxResults", "50")
	var raw struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				PublishedAt          time.Time `json:"publishedAt"`
				CategoryID           string    `json:"categoryId"`
				Tags                 []string  `json:"tags"`
				ChannelID            string    `json:"channelId"`
				LiveBroadcastContent string    `json:"liveBroadcastContent"`
			} `json:"snippet"`
			Statistics struct {
				ViewCount    *string `json:"viewCount"`
				LikeCount    *string `json:"likeCount"`
				CommentCount *string `json:"commentCount"`
			} `json:"statistics"`
			ContentDetails struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := c.get(ctx, "videos", CostVideosList, p, &raw); err != nil {
		return nil, err
	}
	var out []VideoStats
	for _, it := range raw.Items {
		v := VideoStats{
			VideoID: it.ID, PublishedAt: it.Snippet.PublishedAt, CategoryID: it.Snippet.CategoryID,
			TagCount: len(it.Snippet.Tags), ChannelID: it.Snippet.ChannelID, DurationISO: it.ContentDetails.Duration,
			LiveBroadcast: it.Snippet.LiveBroadcastContent,
		}
		v.Views = atoi(it.Statistics.ViewCount)
		if it.Statistics.LikeCount != nil {
			v.Likes, v.HasLikes = atoi(it.Statistics.LikeCount), true
		}
		if it.Statistics.CommentCount != nil {
			v.Comments, v.HasComments = atoi(it.Statistics.CommentCount), true
		}
		out = append(out, v)
	}
	return out, nil
}

// CheckKey makes the cheapest possible call (1 unit) to confirm the key works.
func (c *Client) CheckKey(ctx context.Context) error {
	p := url.Values{}
	p.Set("part", "snippet")
	p.Set("regionCode", "IQ")
	var raw map[string]any
	return c.get(ctx, "videoCategories", CostVideoCategories, p, &raw)
}

func atoi(s *string) int64 {
	if s == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*s, 10, 64)
	return n
}

// Ledger counts quota units per Pacific-time day and persists them in a
// small JSON file so restarts don't lose the count.
type Ledger struct {
	mu      sync.Mutex
	path    string // "" keeps the ledger in memory
	ceiling int
	day     string
	used    map[string]int // method → units
	total   int
}

var pacific = func() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.FixedZone("PST", -8*3600)
	}
	return loc
}()

// PacificDay is the quota day a moment belongs to.
func PacificDay(t time.Time) string { return t.In(pacific).Format("2006-01-02") }

// NewLedger loads (or creates) a ledger. ceiling is the daily unit limit.
func NewLedger(path string, ceiling int) (*Ledger, error) {
	l := &Ledger{path: path, ceiling: ceiling, used: map[string]int{}}
	if path == "" {
		return l, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	var saved struct {
		Day  string         `json:"day"`
		Used map[string]int `json:"used"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("ytapi: reading quota ledger: %w", err)
	}
	l.day, l.used = saved.Day, saved.Used
	if l.used == nil {
		l.used = map[string]int{}
	}
	for _, u := range l.used {
		l.total += u
	}
	return l, nil
}

// Charge reserves units for one call, or returns ErrQuota.
func (l *Ledger) Charge(now time.Time, method string, units int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := PacificDay(now); d != l.day {
		l.day, l.used, l.total = d, map[string]int{}, 0
	}
	if l.total+units > l.ceiling {
		return ErrQuota
	}
	l.used[method] += units
	l.total += units
	return l.saveLocked()
}

// Used returns units used today by method, and the total.
func (l *Ledger) Used(now time.Time) (map[string]int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if PacificDay(now) != l.day {
		return map[string]int{}, 0
	}
	out := map[string]int{}
	for k, v := range l.used {
		out[k] = v
	}
	return out, l.total
}

func (l *Ledger) saveLocked() error {
	if l.path == "" {
		return nil
	}
	data, _ := json.Marshal(map[string]any{"day": l.day, "used": l.used})
	tmp := l.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}
