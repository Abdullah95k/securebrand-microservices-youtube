package ailabel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testKey = "test-key-not-real"

// fakeAPI serves POST /v1/messages with reply(body) and records requests.
type fakeAPI struct {
	srv   *httptest.Server
	calls atomic.Int32
	last  atomic.Value // map[string]any: the last request body
	hdr   atomic.Value // http.Header of the last request
}

func newFakeAPI(t *testing.T, reply func(n int, body map[string]any) (int, string)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1))
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		f.last.Store(body)
		f.hdr.Store(r.Header.Clone())
		status, out := reply(n, body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("retry-after-ms", "1")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func message(stop string, content ...any) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "test-model",
		"content": content, "stop_reason": stop,
		"usage": map[string]any{"input_tokens": 120, "output_tokens": 40},
	})
	return string(b)
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func labels(v ...Verdict) string {
	b, _ := json.Marshal(map[string]any{"labels": v})
	return string(b)
}

func client(t *testing.T, f *fakeAPI, cfg Config) *Client {
	t.Helper()
	cfg.APIKey, cfg.BaseURL = testKey, f.srv.URL
	if cfg.Model == "" {
		cfg.Model = "test-model"
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var titles = []Title{{ID: 1, Text: "أخبار بغداد اليوم"}, {ID: 2, Text: "zq recipe of the day"}, {ID: 3, Text: "زياد"}}

func TestLabelSendsTitlesOnlyAndParsesVerdicts(t *testing.T) {
	f := newFakeAPI(t, func(int, map[string]any) (int, string) {
		return 200, message("end_turn", textBlock(labels(
			Verdict{ID: 1, Answer: "yes", Reason: "Baghdad news"},
			Verdict{ID: 2, Answer: "No", Reason: "generic recipe"},
			Verdict{ID: 3, Answer: "unsure", Reason: "bare name"},
			Verdict{ID: 3, Answer: "yes", Reason: "duplicate id, ignored"},
			Verdict{ID: 9, Answer: "yes", Reason: "unknown id, ignored"},
		)))
	})
	c := client(t, f, Config{Effort: "high", Fallback: true})
	res, err := c.Label(context.Background(), titles)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Verdicts) != 3 || res.Verdicts[0].Answer != Yes || res.Verdicts[1].Answer != No || res.Verdicts[2].Answer != Unsure {
		t.Fatalf("verdicts %+v", res.Verdicts)
	}
	if res.Model != "test-model" || res.Refused || res.InputTokens != 120 || res.OutputTokens != 40 {
		t.Errorf("result %+v", res)
	}

	body := f.last.Load().(map[string]any)
	hdr := f.hdr.Load().(http.Header)
	if hdr.Get("X-Api-Key") != testKey {
		t.Error("the key must go in the x-api-key header")
	}
	if !strings.Contains(hdr.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") || body["fallbacks"] != "default" {
		t.Errorf("fallback not requested: beta %q, fallbacks %v", hdr.Get("Anthropic-Beta"), body["fallbacks"])
	}
	if body["model"] != "test-model" {
		t.Errorf("model %v", body["model"])
	}
	oc, _ := body["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	if oc["effort"] != "high" || format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("output_config %v", oc)
	}
	raw, _ := json.Marshal(body["messages"])
	for _, want := range []string{"أخبار بغداد اليوم", "zq recipe of the day", `\"id\":3`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("messages miss %q: %s", want, raw)
		}
	}
	all, _ := json.Marshal(body)
	if strings.Contains(string(all), testKey) {
		t.Error("the key must not be in the request body")
	}
}

func TestLabelWithoutFallbackOrEffort(t *testing.T) {
	f := newFakeAPI(t, func(int, map[string]any) (int, string) {
		return 200, message("end_turn", textBlock(labels(Verdict{ID: 1, Answer: "yes", Reason: "r"})))
	})
	c := client(t, f, Config{})
	if _, err := c.Label(context.Background(), titles[:1]); err != nil {
		t.Fatal(err)
	}
	body := f.last.Load().(map[string]any)
	oc, _ := body["output_config"].(map[string]any)
	if _, ok := body["fallbacks"]; ok || oc["effort"] != nil {
		t.Errorf("want no fallbacks and no effort: %v", body)
	}
	if strings.Contains(f.hdr.Load().(http.Header).Get("Anthropic-Beta"), "fallback") {
		t.Error("want no fallback beta header")
	}
}

// TestLabelAfterFallback: text before a fallback block came from the model
// that declined and is ignored; the answering model is reported.
func TestLabelAfterFallback(t *testing.T) {
	f := newFakeAPI(t, func(int, map[string]any) (int, string) {
		return 200, message("end_turn",
			textBlock(`{"labels":[{"id":1,"answer":"no"`),
			map[string]any{"type": "fallback", "from": map[string]any{"model": "test-model"}, "to": map[string]any{"model": "fallback-model"}},
			textBlock(labels(Verdict{ID: 1, Answer: "yes", Reason: "Baghdad"})))
	})
	res, err := client(t, f, Config{Fallback: true}).Label(context.Background(), titles[:1])
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "fallback-model" || len(res.Verdicts) != 1 || res.Verdicts[0].Answer != Yes {
		t.Errorf("result %+v", res)
	}
}

func TestLabelRefusalAndUnusableAnswers(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		refused bool
		errAns  bool
	}{
		{"refusal", message("refusal", textBlock("")), true, false},
		{"cut off", message("max_tokens", textBlock(`{"labels":[{"id":1,`)), false, true},
		{"not json", message("end_turn", textBlock("Sure! Here are the labels.")), false, true},
	}
	for _, tc := range cases {
		f := newFakeAPI(t, func(int, map[string]any) (int, string) { return 200, tc.reply })
		res, err := client(t, f, Config{}).Label(context.Background(), titles)
		if res.Refused != tc.refused || errors.Is(err, ErrAnswer) != tc.errAns || (err != nil) != tc.errAns {
			t.Errorf("%s: result %+v, err %v", tc.name, res, err)
		}
	}
}

func TestLabelRetriesRateLimitsAndExplainsErrors(t *testing.T) {
	f := newFakeAPI(t, func(n int, _ map[string]any) (int, string) {
		if n == 1 {
			return 429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
		}
		return 200, message("end_turn", textBlock(labels(Verdict{ID: 1, Answer: "yes", Reason: "r"})))
	})
	if _, err := client(t, f, Config{}).Label(context.Background(), titles[:1]); err != nil || f.calls.Load() != 2 {
		t.Fatalf("want one retry after a 429: calls %d, err %v", f.calls.Load(), err)
	}

	bad := newFakeAPI(t, func(int, map[string]any) (int, string) {
		return 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	})
	_, err := client(t, bad, Config{}).Label(context.Background(), titles[:1])
	if err == nil || !strings.Contains(err.Error(), "API key was not accepted") || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("error %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("an error must never show the key")
	}
	if bad.calls.Load() != 1 {
		t.Errorf("a 401 must not be retried: %d calls", bad.calls.Load())
	}
}

func TestNewChecksConfig(t *testing.T) {
	for _, cfg := range []Config{
		{Model: "m"},  // no key
		{APIKey: "k"}, // no model
		{APIKey: "k", Model: "m", Effort: "turbo"}, // bad effort
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) should fail", Config{Model: cfg.Model, Effort: cfg.Effort})
		}
	}
}
