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
//   - an IP blocked on page requests at least 3 times a day, 3 days in a
//     row, is quarantined (player blocks don't count); it stays out until
//     it is released (Unquarantine).
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
	"sync/atomic"
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
	// country is the exit's ISO 3166-1 alpha-2 country, as the proxy list
	// gives it ("" when not given). It is no secret: it fills
	// provenance.exit_country in the platform contract.
	country string
	// bytes counts the bytes this proxy carried for us (compressed, on the
	// wire), for the proxy-traffic metric and the cost report.
	bytes  atomic.Int64
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

// Country returns the exit's ISO 3166-1 alpha-2 country from the proxy
// list, or "" when the list doesn't say.
func (p *Proxy) Country() string { return p.country }

// AddBytes records n bytes carried by this proxy (request and response, as
// sent and received on the wire).
func (p *Proxy) AddBytes(n int64) {
	if n > 0 {
		p.bytes.Add(n)
	}
}

// Bytes returns the bytes this proxy has carried since start.
func (p *Proxy) Bytes() int64 { return p.bytes.Load() }

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
//	URL [type=dc|isp|res] [subnet=label] [country=XX]
//
// country is the exit's ISO 3166-1 alpha-2 code (IQ, DE…), in either case.
// or the word "direct" for the server's own IP. URL schemes http, https and
// socks5 are supported; a provider's plain HOST:PORT:USER:PASS (or
// HOST:PORT) is read as http://USER:PASS@HOST:PORT. An exit is its login and
// address: two logins on one host and port are two exits (a provider gives
// each login its own exit IP), and by default each is its own subnet group.
// Errors give the line number but never the URL.
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
		subnetTag, country := "", ""
		for _, tag := range fields[1:] {
			k, v, ok := strings.Cut(tag, "=")
			if !ok {
				return nil, fmt.Errorf("proxy line %d: unknown option (want type=…, subnet=… or country=…)", n)
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
			case "country":
				if !isCountryCode(v) {
					return nil, fmt.Errorf("proxy line %d: country %q is not a two-letter ISO code", n, v)
				}
				country = strings.ToUpper(v)
			default:
				return nil, fmt.Errorf("proxy line %d: unknown option %q", n, k)
			}
		}

		var p *Proxy
		if fields[0] == "direct" {
			p = &Proxy{kind: KindDirect, host: "", subnet: "direct"}
			p.id = "direct"
		} else {
			u, err := url.Parse(plainToURL(fields[0]))
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
			p.id = "px-" + shortHash(exitKey(u))
			p.subnet = subnetLabel(u.Hostname(), exitKey(u))
		}
		if subnetTag != "" {
			p.subnet = "sn-" + shortHash("tag:"+subnetTag)
		}
		p.country = country
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
// exitKey is what makes an exit: the login (if any) and host:port. Before
// 14 November it was host:port alone, so two logins on one port collided.
func exitKey(u *url.URL) string {
	if u.User == nil || u.User.Username() == "" {
		return u.Host
	}
	return u.User.Username() + "@" + u.Host
}

// plainToURL turns a provider's plain HOST:PORT:USER:PASS (or HOST:PORT)
// into an http URL; anything else is returned as it is.
func plainToURL(s string) string {
	if strings.Contains(s, "://") || s == "direct" {
		return s
	}
	parts := strings.SplitN(s, ":", 4)
	switch len(parts) {
	case 2:
		return "http://" + parts[0] + ":" + parts[1]
	case 4:
		return "http://" + url.UserPassword(parts[2], parts[3]).String() + "@" + parts[0] + ":" + parts[1]
	}
	return s
}

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

// isCountryCode reports whether s is two ASCII letters.
func isCountryCode(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
