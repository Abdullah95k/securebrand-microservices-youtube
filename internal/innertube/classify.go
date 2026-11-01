package innertube

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
)

// Signal is what a response tells us about the request and the IP.
type Signal int

// Signals.
const (
	SignalOK Signal = iota
	// SignalBlocked: a bot check, CAPTCHA page, 429 or 403. The IP backs off.
	// We never try to answer the check (plan §3).
	SignalBlocked
	// SignalConsent: YouTube's EU cookie-consent page. The IP is unsuitable
	// (plan §7.1: use proxies outside the EU).
	SignalConsent
	// SignalNotFound: the channel, video or feed does not exist (or is gone).
	SignalNotFound
	// SignalServerError: 5xx; worth retrying later.
	SignalServerError
	// SignalBadRequest: other 4xx; our request is wrong, don't retry.
	SignalBadRequest
)

func (s Signal) String() string {
	switch s {
	case SignalOK:
		return "ok"
	case SignalBlocked:
		return "blocked"
	case SignalConsent:
		return "consent"
	case SignalNotFound:
		return "not-found"
	case SignalServerError:
		return "server-error"
	case SignalBadRequest:
		return "bad-request"
	}
	return "unknown"
}

// Classify looks at a response's status, headers and body for block signals.
// body is the decompressed body (may be nil).
func Classify(status int, header http.Header, body []byte) Signal {
	if status == http.StatusNotModified {
		return SignalOK // conditional request: nothing changed
	}
	if status >= 300 && status < 400 {
		loc := strings.ToLower(header.Get("Location"))
		switch {
		case strings.Contains(loc, "google.com/sorry"), strings.Contains(loc, "/sorry/index"):
			return SignalBlocked
		case strings.Contains(loc, "consent.youtube.com"), strings.Contains(loc, "consent.google."):
			return SignalConsent
		}
		return SignalBadRequest // unexpected redirect
	}
	switch {
	case status == http.StatusTooManyRequests, status == http.StatusForbidden:
		return SignalBlocked
	case status == http.StatusNotFound, status == http.StatusGone:
		return SignalNotFound
	case status >= 500:
		return SignalServerError
	case status >= 400:
		return SignalBadRequest
	}
	if looksLikeCaptchaPage(body) {
		return SignalBlocked
	}
	return SignalOK
}

// looksLikeCaptchaPage catches "unusual traffic" pages served with status 200.
func looksLikeCaptchaPage(body []byte) bool {
	if len(body) == 0 || body[0] == '{' {
		return false
	}
	head := body
	if len(head) > 64*1024 {
		head = head[:64*1024]
	}
	lower := bytes.ToLower(head)
	return bytes.Contains(lower, []byte("our systems have detected unusual traffic")) ||
		bytes.Contains(lower, []byte("g-recaptcha")) && bytes.Contains(lower, []byte("/sorry/"))
}

// PlayerSignal inspects a player response's playabilityStatus. A "sign in to
// confirm you're not a bot" answer is a block; "confirm your age" is not (the
// video is simply age-restricted and is skipped, plan §3).
func PlayerSignal(player any) Signal {
	ps := jsonx.Map(player, "playabilityStatus")
	if ps == nil {
		return SignalOK
	}
	status := jsonx.String(ps, "status")
	if status != "LOGIN_REQUIRED" {
		return SignalOK
	}
	if isAgeGate(ps) {
		return SignalOK
	}
	return SignalBlocked
}

func isAgeGate(ps map[string]any) bool {
	if jsonx.Get(ps, "desktopLegacyAgeGateReason") != nil {
		return true
	}
	texts := []string{jsonx.String(ps, "reason")}
	for _, r := range jsonx.FindAll(ps, "reason") {
		texts = append(texts, jsonx.Text(r))
	}
	for _, r := range jsonx.FindAll(ps, "subreason") {
		texts = append(texts, jsonx.Text(r))
	}
	for _, t := range texts {
		l := strings.ToLower(t)
		if strings.Contains(l, "your age") || strings.Contains(l, "age-restricted") ||
			strings.Contains(t, "عمرك") || strings.Contains(t, "العمر") {
			return true
		}
	}
	return false
}
