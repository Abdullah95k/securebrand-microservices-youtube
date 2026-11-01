package phase0

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
)

// Check tests the setup: every proxy fetches YouTube's homepage once (one
// request per IP, never retried on another IP), the API key answers a
// 1-unit call, and the channel list is readable. It prints a plain report.
func Check(ctx context.Context, w io.Writer, proxies []*proxypool.Proxy, baseURL string, api *ytapi.Client, channels []string) bool {
	ok := true
	fmt.Fprintf(w, "Proxies: %d\n", len(proxies))
	for _, px := range proxies {
		// One single-IP pool per proxy, brake off: this is a reachability
		// test, so a block on one IP must not be retried on another.
		pool := proxypool.New([]*proxypool.Proxy{px}, proxypool.Config{DisableBrake: true, ParkAfter: 1}, time.Now().UnixNano())
		cl := innertube.New(innertube.Options{Pool: pool, BaseURL: baseURL})
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		v, err := cl.DetectClientVersion(cctx)
		cancel()
		status := "OK (client version " + v + ")"
		if err != nil {
			ok = false
			status = "FAILED: " + px.ScrubError(err)
		}
		fmt.Fprintf(w, "  %-10s %-6s %s\n", px.ID(), px.Kind(), status)
	}
	if api != nil {
		if err := api.CheckKey(ctx); err != nil {
			ok = false
			fmt.Fprintf(w, "API key: FAILED: %v\n", err)
		} else {
			fmt.Fprintln(w, "API key: OK (used 1 quota unit)")
		}
	} else {
		fmt.Fprintln(w, "API key: not configured (needed for search and the accuracy check)")
	}
	fmt.Fprintf(w, "Channels in list: %d\n", len(channels))
	if len(channels) == 0 {
		ok = false
	}
	if ok {
		fmt.Fprintln(w, "Result: ready for Phase 0")
	} else {
		fmt.Fprintln(w, "Result: fix the FAILED lines above, then run check again")
	}
	return ok
}
