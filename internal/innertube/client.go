// Package innertube talks to YouTube's website endpoints (youtubei/v1/…, the
// watch page, feeds) the way a logged-out browser does, through the proxy
// pool.
//
// Rules (plan §3): no accounts and no account cookies, ever; only the
// anonymous visitor ID each IP receives; block signals are reported to the
// pool and never answered; endpoints that need anti-bot tokens are not used.
package innertube

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// Endpoint is a youtubei/v1 API method.
type Endpoint string

// Endpoints used by the collector.
const (
	Browse Endpoint = "browse"
	Next   Endpoint = "next"
	Player Endpoint = "player"
	Search Endpoint = "search"
	// ResolveURL turns a channel URL or @handle into a channel ID.
	ResolveURL Endpoint = "navigation/resolve_url"
)

func (e Endpoint) class() proxypool.Class {
	switch e {
	case Browse:
		return proxypool.ClassBrowse
	case Next:
		return proxypool.ClassNext
	case Player:
		return proxypool.ClassPlayer
	case Search:
		return proxypool.ClassSearch
	}
	return proxypool.ClassBrowse
}

// Errors.
var (
	// ErrNotFound: the item does not exist or is no longer public.
	ErrNotFound = errors.New("innertube: not found")
	// ErrBlocked is the reason recorded when YouTube sent a block signal.
	ErrBlocked = errors.New("innertube: block signal from YouTube")
	// ErrConsent: the IP got YouTube's EU consent page.
	ErrConsent = errors.New("innertube: consent page (use a proxy outside the EU)")
)

// DefaultUserAgent is a current desktop Chrome user agent.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"

// FallbackClientVersion is used only if detection from the homepage fails.
const FallbackClientVersion = "2.20261028.01.00"

// maxBody caps a decompressed response.
const maxBody = 32 << 20

// Options configures a Client.
type Options struct {
	Pool      *proxypool.Pool
	BaseURL   string // default https://www.youtube.com (tests use a fake server)
	HL        string // interface language, default "ar"
	GL        string // region, default "IQ"
	UserAgent string // default DefaultUserAgent
	// UTCOffsetMinutes is sent in the client context (Iraq: +180).
	UTCOffsetMinutes int
}

// Client is safe for concurrent use.
type Client struct {
	opt Options

	mu            sync.Mutex
	version       string
	versionAt     time.Time
	visitorByIPID map[string]string
}

// New creates a client.
func New(opt Options) *Client {
	if opt.BaseURL == "" {
		opt.BaseURL = "https://www.youtube.com"
	}
	opt.BaseURL = strings.TrimRight(opt.BaseURL, "/")
	if opt.HL == "" {
		opt.HL = "ar"
	}
	if opt.GL == "" {
		opt.GL = "IQ"
	}
	if opt.UserAgent == "" {
		opt.UserAgent = DefaultUserAgent
	}
	if opt.UTCOffsetMinutes == 0 {
		opt.UTCOffsetMinutes = 180
	}
	return &Client{opt: opt, visitorByIPID: map[string]string{}}
}

// Response is one successful (or not-found) answer.
type Response struct {
	Status    int
	Header    http.Header
	Signal    Signal
	Body      []byte // decompressed body
	Data      any    // parsed JSON for youtubei calls; nil for pages
	WireBytes int64  // bytes on the wire (compressed)
	Proxy     string // proxy ID that answered
	Elapsed   time.Duration
	Attempt   proxypool.Result
}

// Call sends a youtubei/v1 request. payload holds the endpoint-specific
// fields (browseId, continuation, videoId, query…); the client context is
// added automatically.
func (c *Client) Call(ctx context.Context, ep Endpoint, payload map[string]any) (*Response, error) {
	return c.CallPrefer(ctx, ep, payload, "")
}

// CallPrefer is Call sent through the preferred proxy when it is healthy.
// Pass the previous response's Proxy to keep a chain of continuation
// requests on one IP and one anonymous visitor ID, as a browser does while
// scrolling.
func (c *Client) CallPrefer(ctx context.Context, ep Endpoint, payload map[string]any, prefer string) (*Response, error) {
	version := c.clientVersion(ctx)
	body := map[string]any{}
	for k, v := range payload {
		body[k] = v
	}
	body["context"] = map[string]any{
		"client": map[string]any{
			"clientName":       "WEB",
			"clientVersion":    version,
			"hl":               c.opt.HL,
			"gl":               c.opt.GL,
			"utcOffsetMinutes": c.opt.UTCOffsetMinutes,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := c.opt.BaseURL + "/youtubei/v1/" + string(ep) + "?prettyPrint=false"
	return c.do(ctx, ep.class(), func(px *proxypool.Proxy) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		c.browserHeaders(req, px)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-YouTube-Client-Name", "1")
		req.Header.Set("X-YouTube-Client-Version", version)
		req.Header.Set("Origin", c.opt.BaseURL)
		return req, nil
	}, ep == Player, true, prefer)
}

// Get fetches a page or feed under the base URL (for example
// "/watch?v=…" or "/feeds/videos.xml?channel_id=…") as endpoint class cl.
// Extra headers (for conditional requests) may be given.
func (c *Client) Get(ctx context.Context, cl proxypool.Class, path string, extra http.Header) (*Response, error) {
	url := c.opt.BaseURL + path
	return c.do(ctx, cl, func(px *proxypool.Proxy) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		c.browserHeaders(req, px)
		for k, vs := range extra {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		return req, nil
	}, false, false, "")
}

func (c *Client) browserHeaders(req *http.Request, px *proxypool.Proxy) {
	req.Header.Set("User-Agent", c.opt.UserAgent)
	req.Header.Set("Accept-Language", c.opt.HL+","+c.opt.HL+"-"+c.opt.GL+";q=0.9,en;q=0.5")
	req.Header.Set("Accept-Encoding", "gzip")
	if vd := c.visitor(px.ID()); vd != "" {
		req.Header.Set("X-Goog-Visitor-Id", vd)
	}
}

func (c *Client) do(ctx context.Context, cl proxypool.Class, build func(*proxypool.Proxy) (*http.Request, error), isPlayer, isJSON bool, prefer string) (*Response, error) {
	var out *Response
	attempt, err := c.opt.Pool.DoPrefer(ctx, cl, prefer, func(ctx context.Context, px *proxypool.Proxy) (proxypool.Outcome, error) {
		req, err := build(px)
		if err != nil {
			return proxypool.Fatal, err
		}
		start := time.Now()
		resp, err := px.Client().Do(req)
		if err != nil {
			return networkOutcome(ctx, err), errors.New(px.ScrubError(err))
		}
		wire, body, rerr := readBody(resp)
		resp.Body.Close()
		if rerr != nil {
			return proxypool.Transient, errors.New(px.ScrubError(rerr))
		}
		r := &Response{Status: resp.StatusCode, Header: resp.Header, Body: body, WireBytes: wire, Proxy: px.ID(), Elapsed: time.Since(start)}
		r.Signal = Classify(resp.StatusCode, resp.Header, body)
		switch r.Signal {
		case SignalBlocked:
			return proxypool.Blocked, ErrBlocked
		case SignalConsent:
			return proxypool.ProxyFailed, ErrConsent
		case SignalServerError:
			return proxypool.Transient, fmt.Errorf("innertube: HTTP %d", resp.StatusCode)
		case SignalBadRequest:
			if u, err := resp.Location(); err == nil {
				return proxypool.Fatal, fmt.Errorf("innertube: HTTP %d redirect to %s%s", resp.StatusCode, u.Host, u.Path)
			}
			return proxypool.Fatal, fmt.Errorf("innertube: HTTP %d", resp.StatusCode)
		case SignalNotFound:
			out = r
			return proxypool.OK, nil
		}
		if isJSON && resp.StatusCode != http.StatusNotModified {
			data, err := jsonx.Decode(body)
			if err != nil {
				return proxypool.Fatal, fmt.Errorf("innertube: response is not JSON: %w", err)
			}
			r.Data = data
			if vd := jsonx.String(data, "responseContext", "visitorData"); vd != "" {
				c.setVisitor(px.ID(), vd)
			}
			if isPlayer && PlayerSignal(data) == SignalBlocked {
				r.Signal = SignalBlocked
				return proxypool.Blocked, ErrBlocked
			}
		}
		out = r
		return proxypool.OK, nil
	})
	if out != nil {
		out.Attempt = attempt
	}
	if err != nil {
		return out, err
	}
	if out.Signal == SignalNotFound {
		return out, ErrNotFound
	}
	return out, nil
}

// networkOutcome tells proxy failures apart from YouTube-side timeouts.
func networkOutcome(ctx context.Context, err error) proxypool.Outcome {
	if ctx.Err() != nil {
		return proxypool.Fatal
	}
	msg := err.Error()
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "proxyconnect" {
		return proxypool.ProxyFailed
	}
	if strings.Contains(msg, "proxyconnect") || strings.Contains(msg, "Proxy Authentication Required") ||
		strings.Contains(msg, "socks connect") || strings.Contains(msg, "proxy:") {
		return proxypool.ProxyFailed
	}
	return proxypool.Transient
}

func readBody(resp *http.Response) (wire int64, body []byte, err error) {
	counter := &countingReader{r: io.LimitReader(resp.Body, maxBody)}
	var r io.Reader = counter
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(counter)
		if err != nil {
			return counter.n, nil, err
		}
		defer zr.Close()
		r = io.LimitReader(zr, maxBody)
	}
	body, err = io.ReadAll(r)
	return counter.n, body, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *Client) visitor(proxyID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.visitorByIPID[proxyID]
}

func (c *Client) setVisitor(proxyID, vd string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.visitorByIPID[proxyID] = vd
}

var (
	versionRe = regexp.MustCompile(`"INNERTUBE_CLIENT_VERSION"\s*:\s*"([0-9][0-9.]*)"`)
	visitorRe = regexp.MustCompile(`"VISITOR_DATA"\s*:\s*"([A-Za-z0-9_%=-]+)"`)
)

// clientVersion returns the web client version, read from YouTube's homepage
// at most once a day (plan §8.11).
func (c *Client) clientVersion(ctx context.Context) string {
	c.mu.Lock()
	if c.version != "" && time.Since(c.versionAt) < 24*time.Hour {
		v := c.version
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()

	v, _ := c.DetectClientVersion(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if v == "" {
		v = FallbackClientVersion
	}
	c.version, c.versionAt = v, time.Now()
	return v
}

// DetectClientVersion fetches the homepage and reads the current web client
// version (and the visitor ID offered to that IP).
func (c *Client) DetectClientVersion(ctx context.Context) (string, error) {
	// The homepage is a page, like the watch page: a block there must not
	// stop API calls (health is tracked per endpoint class).
	resp, err := c.Get(ctx, proxypool.ClassWatch, "/", nil)
	if err != nil {
		return "", err
	}
	if m := visitorRe.FindSubmatch(resp.Body); m != nil {
		c.setVisitor(resp.Proxy, string(m[1]))
	}
	m := versionRe.FindSubmatch(resp.Body)
	if m == nil {
		return "", errors.New("innertube: client version not found on homepage")
	}
	return string(m[1]), nil
}

// ClientVersion reports the version currently in use (for the Phase 0 report).
func (c *Client) ClientVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}
