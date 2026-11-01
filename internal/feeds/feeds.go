// Package feeds reads YouTube's public channel feeds
// (/feeds/videos.xml?channel_id=…). A feed lists a channel's latest ~15
// uploads with exact view and like counts, so it is both the backup
// new-upload detector and a cheap source of stats for recent videos
// (plan §8.5).
package feeds

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// Entry is one upload in a feed.
type Entry struct {
	VideoID     string
	ChannelID   string
	Title       string
	Description string
	Link        string
	Thumbnail   string
	Published   time.Time
	Updated     time.Time
	Views       int64
	Likes       int64
	HasStats    bool
}

// Feed is a parsed channel feed.
type Feed struct {
	ChannelID string
	Title     string
	Entries   []Entry
}

type xmlFeed struct {
	ChannelID string     `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
	Title     string     `xml:"title"`
	Entries   []xmlEntry `xml:"entry"`
}

type xmlEntry struct {
	VideoID   string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
	ChannelID string `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
	Title     string `xml:"title"`
	Link      struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
	Group     struct {
		Description string `xml:"http://search.yahoo.com/mrss/ description"`
		Thumbnail   struct {
			URL string `xml:"url,attr"`
		} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
		Community struct {
			StarRating struct {
				Count *int64 `xml:"count,attr"`
			} `xml:"http://search.yahoo.com/mrss/ starRating"`
			Statistics struct {
				Views *int64 `xml:"views,attr"`
			} `xml:"http://search.yahoo.com/mrss/ statistics"`
		} `xml:"http://search.yahoo.com/mrss/ community"`
	} `xml:"http://search.yahoo.com/mrss/ group"`
}

// Parse reads a feed document.
func Parse(data []byte) (*Feed, error) {
	var x xmlFeed
	dec := xml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&x); err != nil {
		return nil, fmt.Errorf("feeds: invalid feed XML: %w", err)
	}
	f := &Feed{ChannelID: x.ChannelID, Title: strings.TrimSpace(x.Title)}
	for _, e := range x.Entries {
		en := Entry{
			VideoID:     e.VideoID,
			ChannelID:   e.ChannelID,
			Title:       strings.TrimSpace(e.Title),
			Description: e.Group.Description,
			Link:        e.Link.Href,
			Thumbnail:   e.Group.Thumbnail.URL,
			Published:   parseTime(e.Published),
			Updated:     parseTime(e.Updated),
		}
		if v := e.Group.Community.Statistics.Views; v != nil {
			en.Views, en.HasStats = *v, true
		}
		if l := e.Group.Community.StarRating.Count; l != nil {
			en.Likes = *l
		}
		if en.VideoID == "" {
			continue
		}
		f.Entries = append(f.Entries, en)
	}
	return f, nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// ErrNotModified means the feed did not change since the last fetch.
var ErrNotModified = errors.New("feeds: not modified")

// Fetcher downloads feeds with conditional requests (ETag / Last-Modified),
// remembering validators per channel.
type Fetcher struct {
	client *innertube.Client

	mu         sync.Mutex
	validators map[string]validator
}

type validator struct{ etag, lastModified string }

// NewFetcher creates a fetcher that sends requests through client.
func NewFetcher(client *innertube.Client) *Fetcher {
	return &Fetcher{client: client, validators: map[string]validator{}}
}

// Result describes one fetch, for the Phase 0 report.
type Result struct {
	Feed      *Feed
	WireBytes int64
	Proxy     string
	Elapsed   time.Duration
}

// Fetch downloads the feed of channelID. It returns ErrNotModified when the
// feed is unchanged and innertube.ErrNotFound for a 404 (YouTube's feed
// service sometimes returns 404s for valid channels for hours, so callers
// should treat it as "try again later", plan §3.4).
func (f *Fetcher) Fetch(ctx context.Context, channelID string) (*Result, error) {
	path := "/feeds/videos.xml?channel_id=" + url.QueryEscape(channelID)
	h := http.Header{}
	f.mu.Lock()
	v := f.validators[channelID]
	f.mu.Unlock()
	if v.etag != "" {
		h.Set("If-None-Match", v.etag)
	}
	if v.lastModified != "" {
		h.Set("If-Modified-Since", v.lastModified)
	}
	resp, err := f.client.Get(ctx, proxypool.ClassRSS, path, h)
	if resp == nil {
		return nil, err
	}
	res := &Result{WireBytes: resp.WireBytes, Proxy: resp.Proxy, Elapsed: resp.Elapsed}
	if err != nil {
		return res, err
	}
	if resp.Status == http.StatusNotModified {
		return res, ErrNotModified
	}
	feed, err := Parse(resp.Body)
	if err != nil {
		return res, err
	}
	res.Feed = feed
	f.mu.Lock()
	f.validators[channelID] = validator{etag: resp.Header.Get("ETag"), lastModified: resp.Header.Get("Last-Modified")}
	f.mu.Unlock()
	return res, nil
}
