// Package proxypool spreads scraping requests over a pool of proxy IPs and
// implements the pacing and failover rules of the plan (§3):
//
//   - every IP has its own slow budget (minimum interval plus random jitter)
//     and handles one request at a time;
//   - a blocked IP cools down (30 min, 2 h, 12 h, 24 h on repeat) and the work
//     moves to another healthy IP after a short random delay;
//   - a request blocked on 3 different IPs is parked (ErrParked);
//   - health is tracked per IP and per endpoint class;
//   - when several IPs of one subnet are blocked, the whole subnet cools down;
//   - a pool brake slows everything when >20% of IPs are blocked within an
//     hour, and pauses everything when >50% are.
//
// The pool never solves CAPTCHAs or bot checks; it only decides which IP may
// send the next request and when.
package proxypool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Kind is the type of IP behind a proxy.
type Kind string

// Proxy kinds.
const (
	KindDatacenter  Kind = "dc"
	KindISP         Kind = "isp"
	KindResidential Kind = "res"
	KindDirect      Kind = "direct" // the server's own IP, no proxy
)

// Proxy is one exit IP. Its String, LogValue and Format output is only the
// opaque ID, so credentials can't leak through logging by accident.
type Proxy struct {
	id     string
	kind   Kind
	subnet string // opaque label; raw addresses are never exposed
	url    *url.URL
	host   string // host:port of the proxy, used only to scrub errors
	client *http.Client
}

// ID is a short stable identifier derived from the proxy's host and port.
func (p *Proxy) ID() string { return p.id }

// Kind returns the IP type.
func (p *Proxy) Kind() Kind { return p.kind }

// Subnet returns an opaque label for the proxy's subnet.
func (p *Proxy) Subnet() string { return p.subnet }

// Client returns the HTTP client that sends requests through this proxy. Each
// proxy has its own cookie jar, so anonymous visitor IDs are never shared
// between IPs. Redirects are not followed: callers inspect them to detect bot
// checks and consent pages.
func (p *Proxy) Client() *http.Client { return p.client }

func (p *Proxy) String() string { return p.id }

// GoString keeps %#v from printing the URL.
func (p *Proxy) GoString() string { return "proxy(" + p.id + ")" }

// LogValue keeps slog from printing the URL.
func (p *Proxy) LogValue() slog.Value { return slog.StringValue(p.id) }

// ScrubError returns err's message with the proxy's address replaced by its
// ID, so errors from the HTTP stack can be logged safely.
func (p *Proxy) ScrubError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if p.url != nil {
		msg = strings.ReplaceAll(msg, p.url.String(), p.id)
		msg = strings.ReplaceAll(msg, p.url.Redacted(), p.id)
		if u := p.url.User; u != nil {
			if pw, ok := u.Password(); ok && pw != "" {
				msg = strings.ReplaceAll(msg, pw, "***")
			}
		}
	}
	if p.host != "" {
		msg = strings.ReplaceAll(msg, p.host, p.id)
		if h, _, err := net.SplitHostPort(p.host); err == nil && h != "" {
			msg = strings.ReplaceAll(msg, h, p.id)
		}
	}
	return msg
}

// ParseList parses proxy lines (comments and blank lines already removed, for
// example by secrets.ReadLines). Each line is
//
//	URL [type=dc|isp|res] [subnet=label]
//
// or the word "direct" for the server's own IP. URL schemes http, https and
// socks5 are supported. Errors give the line number but never the URL.
func ParseList(lines []string) ([]*Proxy, error) {
	var out []*Proxy
	seen := map[string]int{}
	for i, line := range lines {
		n := i + 1
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		kind := KindDatacenter
		subnetTag := ""
		for _, tag := range fields[1:] {
			k, v, ok := strings.Cut(tag, "=")
			if !ok {
				return nil, fmt.Errorf("proxy line %d: unknown option (want type=… or subnet=…)", n)
			}
			switch k {
			case "type":
				switch Kind(v) {
				case KindDatacenter, KindISP, KindResidential:
					kind = Kind(v)
				default:
					return nil, fmt.Errorf("proxy line %d: unknown type %q (want dc, isp or res)", n, v)
				}
			case "subnet":
				if v == "" {
					return nil, fmt.Errorf("proxy line %d: empty subnet label", n)
				}
				subnetTag = v
			default:
				return nil, fmt.Errorf("proxy line %d: unknown option %q", n, k)
			}
		}

		var p *Proxy
		if fields[0] == "direct" {
			p = &Proxy{kind: KindDirect, host: "", subnet: "direct"}
			p.id = "direct"
		} else {
			u, err := url.Parse(fields[0])
			// Never wrap url.Parse errors: they contain the raw URL.
			if err != nil || u.Host == "" {
				return nil, fmt.Errorf("proxy line %d: invalid proxy URL", n)
			}
			switch u.Scheme {
			case "http", "https", "socks5":
			default:
				return nil, fmt.Errorf("proxy line %d: unsupported scheme (want http, https or socks5)", n)
			}
			if u.Port() == "" {
				return nil, fmt.Errorf("proxy line %d: proxy URL needs a port", n)
			}
			p = &Proxy{kind: kind, url: u, host: u.Host}
			p.id = "px-" + shortHash(u.Host)
			p.subnet = subnetLabel(u.Hostname(), u.Host)
		}
		if subnetTag != "" {
			p.subnet = "sn-" + shortHash("tag:"+subnetTag)
		}
		if prev, dup := seen[p.id]; dup {
			if p.kind == KindDirect {
				return nil, fmt.Errorf("proxy line %d: 'direct' listed twice (also line %d)", n, prev)
			}
			return nil, fmt.Errorf("proxy line %d: same proxy as line %d", n, prev)
		}
		seen[p.id] = n
		p.client = newClient(p.url)
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no proxies configured")
	}
	return out, nil
}

// subnetLabel groups IPv4 addresses by /24 and IPv6 by /64. A proxy given by
// hostname (for example a provider gateway where each port is a different
// exit IP) is treated as its own subnet unless a subnet= tag says otherwise.
func subnetLabel(hostname, hostport string) string {
	if ip := net.ParseIP(hostname); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return "sn-" + shortHash(v4.Mask(net.CIDRMask(24, 32)).String()+"/24")
		}
		return "sn-" + shortHash(ip.Mask(net.CIDRMask(64, 128)).String()+"/64")
	}
	return "sn-" + shortHash("host:"+hostport)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:3])
}

func newClient(proxyURL *url.URL) *http.Client {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	jar, _ := cookiejar.New(nil) // never fails with nil options
	return &http.Client{
		Transport: tr,
		Jar:       jar,
		Timeout:   60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
