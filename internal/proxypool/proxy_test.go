package proxypool

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Proxy URLs in tests use documentation addresses (RFC 5737) and the
// placeholder credentials the fixture scanner allows.
const testCreds = "user:password"

func proxyLine(host string, opts ...string) string {
	return strings.Join(append([]string{"http://" + testCreds + "@" + host}, opts...), " ")
}

func TestParseList(t *testing.T) {
	ps, err := ParseList([]string{
		proxyLine("192.0.2.10:8080"),
		proxyLine("192.0.2.11:8080", "type=isp"),
		proxyLine("198.51.100.7:3128", "type=res", "subnet=lab-a"),
		"socks5://" + testCreds + "@gw.example.net:10001",
		"direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 5 {
		t.Fatalf("got %d proxies", len(ps))
	}
	if ps[0].Kind() != KindDatacenter || ps[1].Kind() != KindISP || ps[2].Kind() != KindResidential || ps[4].Kind() != KindDirect {
		t.Errorf("kinds = %v %v %v %v", ps[0].Kind(), ps[1].Kind(), ps[2].Kind(), ps[4].Kind())
	}
	if ps[0].Subnet() != ps[1].Subnet() {
		t.Error("192.0.2.10 and .11 should share a /24 subnet label")
	}
	if ps[0].Subnet() == ps[2].Subnet() {
		t.Error("different networks should have different subnet labels")
	}
	if !strings.HasPrefix(ps[0].ID(), "px-") || ps[0].ID() == ps[1].ID() {
		t.Errorf("ids = %s %s", ps[0].ID(), ps[1].ID())
	}
	// IDs are stable: derived from the login and host:port, not list order.
	again, _ := ParseList([]string{proxyLine("192.0.2.11:8080", "type=isp")})
	if again[0].ID() != ps[1].ID() {
		t.Error("proxy ID should not depend on list order")
	}
	if ps[4].ID() != "direct" {
		t.Errorf("direct id = %s", ps[4].ID())
	}
}

// The platform's proxy secret puts a country on each line (REQ-021).
func TestParseListCountry(t *testing.T) {
	ps, err := ParseList([]string{
		proxyLine("192.0.2.30:8080", "type=isp", "subnet=lab-b", "country=IQ"),
		proxyLine("192.0.2.31:8080", "country=de", "type=dc"),
		proxyLine("192.0.2.32:8080"),
		"direct country=IQ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{ps[0].Country(), ps[1].Country(), ps[2].Country(), ps[3].Country()}; got[0] != "IQ" || got[1] != "DE" ||
		got[2] != "" || got[3] != "IQ" {
		t.Errorf("countries = %q; want IQ, DE (upper case), empty when not given, IQ for direct", got)
	}
	if ps[0].Kind() != KindISP || ps[1].Kind() != KindDatacenter {
		t.Errorf("kinds = %v %v; options may come in any order", ps[0].Kind(), ps[1].Kind())
	}
	for _, bad := range []string{"country=", "country=IRQ", "country=I1", "country=عر"} {
		if _, err := ParseList([]string{proxyLine("192.0.2.33:8080", bad)}); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// A provider's plain HOST:PORT:USER:PASS lines, and two logins on one host
// and port (each its own exit IP): 40 exits load as 40 (14 November).
func TestParseListPlainLinesAndTwoLogins(t *testing.T) {
	ps, err := ParseList([]string{
		"gate.example.net:10011:user-a:pa:ss type=isp country=US",
		"gate.example.net:10011:user-b:pw2 type=isp country=US",
		"gate.example.net:10012:user-a:pa:ss subnet=pair-1",
		"gate.example.net:10013:user-b:pw2 subnet=pair-1",
		"gate.example.net:10014",
		"http://" + testCreds + "@gate.example.net:10015",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 6 {
		t.Fatalf("got %d exits", len(ps))
	}
	u := ps[0].url
	if u.Scheme != "http" || u.Host != "gate.example.net:10011" || u.User.Username() != "user-a" {
		t.Errorf("plain line read as %s://%s@%s", u.Scheme, u.User.Username(), u.Host)
	}
	if pw, _ := u.User.Password(); pw != "pa:ss" {
		t.Error("a password may hold a colon")
	}
	if ps[0].ID() == ps[1].ID() || ps[0].Subnet() == ps[1].Subnet() {
		t.Error("two logins on one host and port are two exits, each its own subnet group by default")
	}
	if ps[2].Subnet() != ps[3].Subnet() {
		t.Error("a subnet= label groups exits")
	}
	if ps[0].Kind() != KindISP || ps[0].Country() != "US" || ps[4].url.User != nil {
		t.Errorf("options: %v %q; a HOST:PORT line has no login", ps[0].Kind(), ps[0].Country())
	}
	// The same login and address twice is still one exit listed twice.
	if _, err := ParseList([]string{"gate.example.net:10011:user-a:x", "gate.example.net:10011:user-a:y"}); err == nil ||
		strings.Contains(err.Error(), "user-a") || strings.Contains(err.Error(), "gate.example.net") {
		t.Errorf("a duplicate must be refused, by line number only: %v", err)
	}
}

func TestParseListErrorsNeverLeakURL(t *testing.T) {
	secret := "hunter2xyz"
	bad := []string{
		"http://user:" + secret + "@%zz:80",         // unparsable
		"ftp://user:" + secret + "@192.0.2.1:21",    // bad scheme
		"http://user:" + secret + "@192.0.2.1",      // no port
		"http://user:" + secret + "@192.0.2.1:80 x", // bad option
		"http://user:" + secret + "@192.0.2.1:80 type=mobile",
		"http://user:" + secret + "@192.0.2.1:80 country=XYZ",
	}
	for _, line := range bad {
		_, err := ParseList([]string{line})
		if err == nil {
			t.Errorf("want error for line %d", len(line))
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "192.0.2.1") {
			t.Errorf("error leaks URL parts: %v", err)
		}
	}
	if _, err := ParseList([]string{proxyLine("192.0.2.1:80"), proxyLine("192.0.2.1:80")}); err == nil {
		t.Error("want error for duplicate proxy")
	}
	if _, err := ParseList(nil); err == nil {
		t.Error("want error for empty list")
	}
}

func TestProxyNeverPrintsURL(t *testing.T) {
	ps, err := ParseList([]string{proxyLine("192.0.2.20:8080")})
	if err != nil {
		t.Fatal(err)
	}
	px := ps[0]
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "proxy", px)
	outputs := []string{
		fmt.Sprint(px), fmt.Sprintf("%v %+v %#v %s", px, px, px, px), buf.String(),
		fmt.Sprintf("%+v", New(ps, Config{}, 1).Stats()),
	}
	for _, out := range outputs {
		if strings.Contains(out, "password") || strings.Contains(out, "192.0.2.20") {
			t.Errorf("output leaks proxy address or credentials: %q", out)
		}
	}
}

func TestScrubError(t *testing.T) {
	ps, _ := ParseList([]string{proxyLine("192.0.2.30:8080")})
	px := ps[0]
	u, _ := url.Parse("http://" + testCreds + "@192.0.2.30:8080")
	err := errors.New("proxyconnect tcp: dial tcp 192.0.2.30:8080: refused via " + u.String() + " (" + u.Redacted() + ")")
	got := px.ScrubError(err)
	if strings.Contains(got, "192.0.2.30") || strings.Contains(got, "password") {
		t.Errorf("scrubbed error still leaks: %q", got)
	}
	if !strings.Contains(got, px.ID()) {
		t.Errorf("scrubbed error should name the proxy ID: %q", got)
	}
}

// TestClientUsesProxy checks that requests really go through the configured
// proxy, with credentials, and that redirects are not followed.
func TestClientUsesProxy(t *testing.T) {
	var sawAuth, sawTarget string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Proxy-Authorization")
		sawTarget = r.URL.String()
		w.Header().Set("Location", "https://www.google.com/sorry/index")
		w.WriteHeader(http.StatusFound)
	}))
	defer proxySrv.Close()

	host := strings.TrimPrefix(proxySrv.URL, "http://")
	ps, err := ParseList([]string{"http://" + testCreds + "@" + host})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ps[0].Client().Get("http://www.youtube.example/watch?v=x")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d; redirects must not be followed", resp.StatusCode)
	}
	if sawTarget != "http://www.youtube.example/watch?v=x" {
		t.Errorf("proxy saw target %q", sawTarget)
	}
	if !strings.HasPrefix(sawAuth, "Basic ") {
		t.Errorf("proxy did not receive credentials")
	}
}

func TestEachProxyHasOwnCookieJar(t *testing.T) {
	ps, _ := ParseList([]string{proxyLine("192.0.2.40:8080"), proxyLine("192.0.2.41:8080")})
	if ps[0].Client().Jar == ps[1].Client().Jar {
		t.Error("proxies must not share a cookie jar (visitor IDs per IP)")
	}
}
