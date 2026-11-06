package worker_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
)

func TestSystemic(t *testing.T) {
	systemic := []error{
		proxypool.ErrParked,
		fmt.Errorf("browse: %w", proxypool.ErrNoProxy),
		fmt.Errorf("next: %w: %w", proxypool.ErrProxyFailures, errors.New("connection refused")),
		fmt.Errorf("player: %w (3): %w", proxypool.ErrTransient, errors.New("innertube: HTTP 503")),
		fmt.Errorf("watch: %w", innertube.ErrBlocked),
		innertube.ErrConsent,
		fmt.Errorf("query: %w", context.DeadlineExceeded),
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
	}
	for _, err := range systemic {
		if !worker.Systemic(err) {
			t.Errorf("%v: want systemic (must never kill a job)", err)
		}
	}
	item := []error{
		errors.New("ytparse: watch page has no video ID"),
		fmt.Errorf("writer: channel UCfake0000000000000001 is not in the database"),
		fmt.Errorf("request failed: %w", errors.New("innertube: HTTP 400")),
	}
	for _, err := range item {
		if worker.Systemic(err) {
			t.Errorf("%v: want an item failure (counts towards the limit)", err)
		}
	}
	if worker.Systemic(nil) {
		t.Error("nil is not a failure")
	}
}
