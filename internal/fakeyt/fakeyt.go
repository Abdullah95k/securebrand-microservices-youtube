// Package fakeyt is a fake YouTube for tests (plan §8.12). It serves only
// generated data: fake channel IDs start with "UCfake", and nothing here is
// captured from the real site.
//
// It imitates the homepage (client version and visitor ID), the youtubei/v1
// endpoints, channel feeds, and block responses (429, 403, the "sorry"
// redirect, a CAPTCHA page, the player's bot check, the EU consent redirect).
// NewForwardProxy provides plain HTTP proxies so tests exercise the real
// proxy path.
package fakeyt

import (
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Default values served by a new Server.
const (
	DefaultClientVersion = "2.20261101.00.00"
	DefaultVisitorData   = "CgtGQUtFVklTSVRPUg%3D%3D"
)

// Block is a kind of block or interstitial response.
type Block int

// Block kinds.
const (
	Block429 Block = iota + 1
	Block403
	BlockSorryRedirect
	BlockCaptchaPage
	BlockPlayerBotCheck // 200 with playabilityStatus LOGIN_REQUIRED (bot)
	ConsentRedirect
	ServerError
)

// Handler answers a youtubei/v1 request; body is the decoded JSON request.
type Handler func(body map[string]any) (status int, response any)

// Recorded is one request the server received.
type Recorded struct {
	Method   string
	Path     string
	Endpoint string // youtubei endpoint, "home", "feed" or "page"
	Header   http.Header
	Body     map[string]any
	Via      string // X-Fake-Proxy header set by NewForwardProxy
}

// FeedEntry is one generated upload in a channel feed.
type FeedEntry struct {
	VideoID   string
	ChannelID string
	Title     string
	Published time.Time
	Views     int64
	Likes     int64
}

type blockRule struct {
	endpoint string // "" matches every endpoint
	kind     Block
	left     int
}

// Server is the fake. Create it with New and Close it when done.
type Server struct {
	URL string
	srv *httptest.Server

	mu            sync.Mutex
	clientVersion string
	visitorData   string
	handlers      map[string]Handler
	feeds         map[string][]FeedEntry
	blocks        []*blockRule
	requests      []Recorded
}

// New starts a fake server.
func New() *Server {
	s := &Server{
		clientVersion: DefaultClientVersion,
		visitorData:   DefaultVisitorData,
		handlers:      map[string]Handler{},
		feeds:         map[string][]FeedEntry{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Handle sets the response for a youtubei/v1 endpoint ("browse", "next"…).
func (s *Server) Handle(endpoint string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[endpoint] = h
}

// SetFeed sets the uploads served for a channel's feed.
func (s *Server) SetFeed(channelID string, entries []FeedEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feeds[channelID] = entries
}

// BlockNext makes the next n requests to endpoint ("" for any; also "home"
// and "feed") answer with kind.
func (s *Server) BlockNext(endpoint string, n int, kind Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = append(s.blocks, &blockRule{endpoint: endpoint, kind: kind, left: n})
}

// Requests returns a copy of everything received so far.
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recorded(nil), s.requests...)
}

func (s *Server) takeBlock(endpoint string) Block {
	for _, b := range s.blocks {
		if b.left > 0 && (b.endpoint == "" || b.endpoint == endpoint) {
			b.left--
			return b.kind
		}
	}
	return 0
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	endpoint := "page"
	switch {
	case r.URL.Path == "/":
		endpoint = "home"
	case strings.HasPrefix(r.URL.Path, "/youtubei/v1/"):
		endpoint = strings.TrimPrefix(r.URL.Path, "/youtubei/v1/")
	case r.URL.Path == "/feeds/videos.xml":
		endpoint = "feed"
	case strings.HasPrefix(r.URL.Path, "/youtube/v3/"):
		// Official Data API routes, answered by handlers named "api:<method>".
		endpoint = "api:" + strings.TrimPrefix(r.URL.Path, "/youtube/v3/")
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
	}

	s.mu.Lock()
	s.requests = append(s.requests, Recorded{
		Method: r.Method, Path: r.URL.RequestURI(), Endpoint: endpoint,
		Header: r.Header.Clone(), Body: body, Via: r.Header.Get("X-Fake-Proxy"),
	})
	block := s.takeBlock(endpoint)
	version, visitor := s.clientVersion, s.visitorData
	handler := s.handlers[endpoint]
	feed, hasFeed := s.feeds[r.URL.Query().Get("channel_id")]
	s.mu.Unlock()

	switch block {
	case Block429:
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	case Block403:
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	case BlockSorryRedirect:
		w.Header().Set("Location", "https://www.google.com/sorry/index?continue=x")
		w.WriteHeader(http.StatusFound)
		return
	case ConsentRedirect:
		w.Header().Set("Location", "https://consent.youtube.com/m?continue=x")
		w.WriteHeader(http.StatusSeeOther)
		return
	case BlockCaptchaPage:
		writeBody(w, r, "text/html", []byte("<html><body>Our systems have detected unusual traffic from your computer network.</body></html>"))
		return
	case ServerError:
		http.Error(w, "backend error", http.StatusServiceUnavailable)
		return
	}

	switch endpoint {
	case "home":
		page := fmt.Sprintf(`<html><script>ytcfg.set({"INNERTUBE_CLIENT_VERSION":"%s","VISITOR_DATA":"%s"});</script></html>`, version, visitor)
		writeBody(w, r, "text/html", []byte(page))
	case "feed":
		if !hasFeed {
			http.NotFound(w, r)
			return
		}
		etag := fmt.Sprintf(`"%d-%d"`, len(feed), feedHash(feed))
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeBody(w, r, "application/atom+xml", FeedXML(r.URL.Query().Get("channel_id"), feed))
	case "page":
		http.NotFound(w, r)
	default:
		if strings.HasPrefix(endpoint, "api:") {
			if handler == nil {
				writeJSON(w, r, http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404}})
				return
			}
			q := map[string]any{}
			for k, v := range r.URL.Query() {
				q[k] = strings.Join(v, ",")
			}
			status, resp := handler(q)
			writeJSON(w, r, status, resp)
			return
		}
		if block == BlockPlayerBotCheck {
			writeJSON(w, r, http.StatusOK, map[string]any{
				"responseContext": map[string]any{"visitorData": visitor},
				"playabilityStatus": map[string]any{
					"status": "LOGIN_REQUIRED",
					"reason": "Sign in to confirm you’re not a bot",
				},
			})
			return
		}
		if handler == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404}})
			return
		}
		status, resp := handler(body)
		if m, ok := resp.(map[string]any); ok {
			if _, has := m["responseContext"]; !has {
				m["responseContext"] = map[string]any{"visitorData": visitor}
			}
		}
		writeJSON(w, r, status, resp)
	}
}

func feedHash(entries []FeedEntry) int64 {
	var h int64
	for _, e := range entries {
		h = h*31 + e.Views + e.Likes + int64(len(e.Title))
	}
	return h
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	data, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	writeStatusBody(w, r, status, data)
}

func writeBody(w http.ResponseWriter, r *http.Request, ctype string, data []byte) {
	w.Header().Set("Content-Type", ctype)
	writeStatusBody(w, r, http.StatusOK, data)
}

func writeStatusBody(w http.ResponseWriter, r *http.Request, status int, data []byte) {
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(status)
		zw := gzip.NewWriter(w)
		zw.Write(data)
		zw.Close()
		return
	}
	w.WriteHeader(status)
	w.Write(data)
}

// FeedXML renders a channel feed in YouTube's Atom format.
func FeedXML(channelID string, entries []FeedEntry) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/" xmlns="http://www.w3.org/2005/Atom">` + "\n")
	fmt.Fprintf(&b, "<yt:channelId>%s</yt:channelId>\n<title>%s</title>\n", esc(channelID), esc("Fake channel "+channelID))
	for _, e := range entries {
		fmt.Fprintf(&b, `<entry>
<yt:videoId>%s</yt:videoId>
<yt:channelId>%s</yt:channelId>
<title>%s</title>
<link rel="alternate" href="https://www.youtube.com/watch?v=%s"/>
<published>%s</published>
<updated>%s</updated>
<media:group>
<media:title>%s</media:title>
<media:thumbnail url="https://i.ytimg.com/vi/%s/hqdefault.jpg" width="480" height="360"/>
<media:description>%s</media:description>
<media:community>
<media:starRating count="%d" average="5.00" min="1" max="5"/>
<media:statistics views="%d"/>
</media:community>
</media:group>
</entry>
`, esc(e.VideoID), esc(e.ChannelID), esc(e.Title), esc(e.VideoID),
			e.Published.UTC().Format(time.RFC3339), e.Published.UTC().Format(time.RFC3339),
			esc(e.Title), esc(e.VideoID), esc("Description of "+e.Title), e.Likes, e.Views)
	}
	b.WriteString("</feed>\n")
	return []byte(b.String())
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ForwardProxy is a minimal HTTP forward proxy for tests. It adds an
// X-Fake-Proxy header naming itself so tests can see which proxy was used.
type ForwardProxy struct {
	Name string
	URL  string // http://127.0.0.1:port
	srv  *httptest.Server

	mu    sync.Mutex
	count int
}

// NewForwardProxy starts a forward proxy for plain-HTTP targets.
func NewForwardProxy(name string) *ForwardProxy {
	fp := &ForwardProxy{Name: name}
	// Never use environment proxies here: tests talk to loopback only.
	tr := &http.Transport{Proxy: nil}
	fp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fp.mu.Lock()
		fp.count++
		fp.mu.Unlock()
		if !r.URL.IsAbs() {
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}
		out, _ := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		out.Header = r.Header.Clone()
		out.Header.Del("Proxy-Authorization")
		out.Header.Set("X-Fake-Proxy", fp.Name)
		resp, err := tr.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	fp.URL = fp.srv.URL
	return fp
}

// Count is the number of requests the proxy forwarded.
func (fp *ForwardProxy) Count() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.count
}

// Close stops the proxy.
func (fp *ForwardProxy) Close() { fp.srv.Close() }

// ProxyLine returns a proxy list line for this proxy, using the placeholder
// credentials the fixture scanner allows.
func (fp *ForwardProxy) ProxyLine() string {
	return "http://user:password@" + strings.TrimPrefix(fp.URL, "http://")
}
