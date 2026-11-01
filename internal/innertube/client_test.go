package innertube

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

func fastPool(t *testing.T, lines []string) *proxypool.Pool {
	t.Helper()
	ps, err := proxypool.ParseList(lines)
	if err != nil {
		t.Fatal(err)
	}
	return proxypool.New(ps, proxypool.Config{
		MinInterval:        time.Millisecond,
		Jitter:             1e-12,
		FailoverDelayMin:   time.Millisecond,
		FailoverDelayMax:   2 * time.Millisecond,
		TransientBackoff:   time.Millisecond,
		ProxyErrorCooldown: time.Hour,
		DisableBrake:       true,
	}, 1)
}

type rig struct {
	yt      *fakeyt.Server
	proxies []*fakeyt.ForwardProxy
	client  *Client
}

func newRig(t *testing.T, nProxies int) *rig {
	t.Helper()
	r := &rig{yt: fakeyt.New()}
	t.Cleanup(r.yt.Close)
	var lines []string
	for i := 0; i < nProxies; i++ {
		fp := fakeyt.NewForwardProxy(string(rune('a' + i)))
		t.Cleanup(fp.Close)
		r.proxies = append(r.proxies, fp)
		lines = append(lines, fp.ProxyLine())
	}
	r.client = New(Options{Pool: fastPool(t, lines), BaseURL: r.yt.URL})
	r.yt.Handle("browse", func(body map[string]any) (int, any) {
		return 200, map[string]any{"contents": map[string]any{"echo": body["browseId"]}}
	})
	return r
}

func (r *rig) endpointRequests(ep string) []fakeyt.Recorded {
	var out []fakeyt.Recorded
	for _, q := range r.yt.Requests() {
		if q.Endpoint == ep {
			out = append(out, q)
		}
	}
	return out
}

func TestCallSendsWebClientContext(t *testing.T) {
	r := newRig(t, 1)
	resp, err := r.client.Call(context.Background(), Browse, map[string]any{"browseId": "UCfakeAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if jsonx.String(resp.Data, "contents", "echo") != "UCfakeAAAAAAAAAAAAAAAAAA" {
		t.Errorf("unexpected response %v", resp.Data)
	}
	if r.client.ClientVersion() != fakeyt.DefaultClientVersion {
		t.Errorf("client version = %q, want the one from the homepage", r.client.ClientVersion())
	}
	reqs := r.endpointRequests("browse")
	if len(reqs) != 1 {
		t.Fatalf("browse requests = %d", len(reqs))
	}
	q := reqs[0]
	if jsonx.String(q.Body, "context", "client", "clientName") != "WEB" ||
		jsonx.String(q.Body, "context", "client", "clientVersion") != fakeyt.DefaultClientVersion ||
		jsonx.String(q.Body, "context", "client", "hl") != "ar" ||
		jsonx.String(q.Body, "context", "client", "gl") != "IQ" {
		t.Errorf("client context = %v", q.Body["context"])
	}
	if q.Header.Get("X-Goog-Visitor-Id") != fakeyt.DefaultVisitorData {
		t.Errorf("visitor id header = %q; want the anonymous ID this IP got", q.Header.Get("X-Goog-Visitor-Id"))
	}
	if q.Header.Get("Authorization") != "" || strings.Contains(q.Header.Get("Cookie"), "SID=") {
		t.Error("no account credentials may ever be sent")
	}
	if q.Via != "a" {
		t.Errorf("request did not go through the proxy (via %q)", q.Via)
	}
	if resp.WireBytes <= 0 || resp.WireBytes >= int64(len(resp.Body))+64 {
		t.Errorf("wire bytes %d vs body %d: compressed size should be measured", resp.WireBytes, len(resp.Body))
	}
}

func TestBlockFailsOverToAnotherProxy(t *testing.T) {
	for _, kind := range []fakeyt.Block{fakeyt.Block429, fakeyt.Block403, fakeyt.BlockSorryRedirect, fakeyt.BlockCaptchaPage} {
		r := newRig(t, 2)
		if _, err := r.client.DetectClientVersion(context.Background()); err != nil {
			t.Fatal(err)
		}
		r.yt.BlockNext("browse", 1, kind)
		resp, err := r.client.Call(context.Background(), Browse, map[string]any{"browseId": "UCfakeBBBBBBBBBBBBBBBBBB"})
		if err != nil {
			t.Fatalf("block %d: %v", kind, err)
		}
		if len(resp.Attempt.BlockedOn) != 1 || resp.Attempt.BlockedOn[0] == resp.Proxy {
			t.Errorf("block %d: want one block then success on another IP, got %+v (final %s)", kind, resp.Attempt, resp.Proxy)
		}
		reqs := r.endpointRequests("browse")
		if len(reqs) != 2 || reqs[0].Via == reqs[1].Via {
			t.Errorf("block %d: the retry must use a different proxy", kind)
		}
	}
}

func TestPlayerBotCheckIsBlockButAgeGateIsNot(t *testing.T) {
	r := newRig(t, 2)
	r.yt.Handle("player", func(map[string]any) (int, any) {
		return 200, map[string]any{"playabilityStatus": map[string]any{"status": "OK"}, "videoDetails": map[string]any{"videoId": "fakevid0001"}}
	})
	r.yt.BlockNext("player", 1, fakeyt.BlockPlayerBotCheck)
	resp, err := r.client.Call(context.Background(), Player, map[string]any{"videoId": "fakevid0001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Attempt.BlockedOn) != 1 {
		t.Errorf("player bot check should count as a block: %+v", resp.Attempt)
	}

	age := map[string]any{"playabilityStatus": map[string]any{"status": "LOGIN_REQUIRED", "reason": "Sign in to confirm your age", "desktopLegacyAgeGateReason": 1}}
	if PlayerSignal(age) != SignalOK {
		t.Error("an age gate is not a block")
	}
}

func TestBlockedEverywhereParks(t *testing.T) {
	r := newRig(t, 2)
	if _, err := r.client.DetectClientVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.yt.BlockNext("browse", 10, fakeyt.Block429)
	_, err := r.client.Call(context.Background(), Browse, map[string]any{"browseId": "UCfakeCCCCCCCCCCCCCCCCCC"})
	if !errors.Is(err, proxypool.ErrParked) {
		t.Fatalf("err = %v, want ErrParked", err)
	}
}

func TestConsentMovesToAnotherProxy(t *testing.T) {
	r := newRig(t, 2)
	if _, err := r.client.DetectClientVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.yt.BlockNext("browse", 1, fakeyt.ConsentRedirect)
	resp, err := r.client.Call(context.Background(), Browse, map[string]any{"browseId": "UCfakeDDDDDDDDDDDDDDDDDD"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempt.ProxyFailures != 1 || len(resp.Attempt.BlockedOn) != 0 {
		t.Errorf("consent page should be a proxy failure, not a block: %+v", resp.Attempt)
	}
}

func TestServerErrorRetries(t *testing.T) {
	r := newRig(t, 1)
	if _, err := r.client.DetectClientVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.yt.BlockNext("browse", 2, fakeyt.ServerError)
	resp, err := r.client.Call(context.Background(), Browse, map[string]any{"browseId": "UCfakeEEEEEEEEEEEEEEEEEE"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempt.Transient != 2 {
		t.Errorf("transient = %d, want 2", resp.Attempt.Transient)
	}
}

func TestNotFound(t *testing.T) {
	r := newRig(t, 1)
	resp, err := r.client.Call(context.Background(), Next, map[string]any{"videoId": "fakevid0404"})
	if !errors.Is(err, ErrNotFound) || resp == nil || resp.Status != http.StatusNotFound {
		t.Fatalf("err = %v resp = %+v; want ErrNotFound with the response", err, resp)
	}
}

// An unexpected redirect is not followed; the error says where it pointed,
// so a failed channel lookup can be diagnosed from the log.
func TestUnexpectedRedirectSaysWhere(t *testing.T) {
	r := newRig(t, 1)
	r.yt.SetRedirect("/@moved", "https://www.youtube.com/@elsewhere?x=1")
	_, err := r.client.Get(context.Background(), proxypool.ClassWatch, "/@moved", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302 redirect to www.youtube.com/@elsewhere") {
		t.Fatalf("err = %v; want the redirect target in the message", err)
	}
}

func TestFallbackVersionWhenHomepageUnreadable(t *testing.T) {
	// Two proxies: the one blocked on the homepage rests, the other serves.
	r := newRig(t, 2)
	r.yt.BlockNext("home", 10, fakeyt.Block429)
	if _, err := r.client.Call(context.Background(), Browse, map[string]any{"browseId": "UCfakeFFFFFFFFFFFFFFFFFF"}); err != nil {
		t.Fatal(err)
	}
	if r.client.ClientVersion() != FallbackClientVersion {
		t.Errorf("version = %q, want fallback", r.client.ClientVersion())
	}
}

func TestClassify(t *testing.T) {
	loc := func(l string) http.Header { h := http.Header{}; h.Set("Location", l); return h }
	tests := []struct {
		status int
		header http.Header
		body   string
		want   Signal
	}{
		{200, nil, `{"ok":true}`, SignalOK},
		{304, nil, "", SignalOK},
		{429, nil, "", SignalBlocked},
		{403, nil, "", SignalBlocked},
		{302, loc("https://www.google.com/sorry/index?continue=x"), "", SignalBlocked},
		{303, loc("https://consent.youtube.com/m"), "", SignalConsent},
		{302, loc("https://example.com/elsewhere"), "", SignalBadRequest},
		{404, nil, "", SignalNotFound},
		{503, nil, "", SignalServerError},
		{400, nil, "", SignalBadRequest},
		{200, nil, "<html>Our systems have detected unusual traffic</html>", SignalBlocked},
		{200, nil, "<html>normal page</html>", SignalOK},
	}
	for _, tc := range tests {
		if got := Classify(tc.status, tc.header, []byte(tc.body)); got != tc.want {
			t.Errorf("Classify(%d, %v, %q) = %v, want %v", tc.status, tc.header, tc.body, got, tc.want)
		}
	}
}
