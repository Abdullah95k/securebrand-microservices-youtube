package worker

import (
	"context"
	"errors"
	"net"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// Systemic reports whether err says nothing about the item itself: a block,
// a pool with no free or working IP, temporary platform errors, a timeout,
// or a database that can't be reached. Such failures still delay the job
// (backoff), but never count towards its failure limit, so an outage can't
// kill every job (REQ-010 fix 3).
func Systemic(err error) bool {
	if err == nil {
		return false
	}
	for _, e := range []error{
		proxypool.ErrParked, proxypool.ErrNoProxy, proxypool.ErrProxyFailures, proxypool.ErrTransient,
		innertube.ErrBlocked, innertube.ErrConsent,
		context.DeadlineExceeded, context.Canceled,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) || pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
