package proxypool

import (
	"context"
	"fmt"
	"time"
)

// Func sends one request through px and reports the outcome. A non-nil error
// is kept as the reason when Do gives up.
type Func func(ctx context.Context, px *Proxy) (Outcome, error)

// Result tells the caller what Do went through, for metrics and the Phase 0
// report.
type Result struct {
	Attempts      int
	BlockedOn     []string // proxy IDs that returned a block signal
	ProxyFailures int
	Transient     int
	FinalProxy    string // proxy ID of the last attempt
}

// Do runs fn with failover (plan §3, D7):
//
//   - Blocked: the IP cools down (handled by the pool) and the request moves to
//     a different IP after a random 5–60 s delay. Once ParkAfter different IPs
//     have blocked it, Do returns ErrParked and the caller retries much later.
//   - ProxyFailed: the IP rests briefly and another IP is tried at once, up to
//     MaxProxyFailures times.
//   - Transient: retried with exponential backoff and jitter, up to
//     TransientAttempts times.
//   - Fatal: returned immediately.
func (p *Pool) Do(ctx context.Context, c Class, fn Func) (Result, error) {
	var res Result
	blocked := map[string]bool{}
	for {
		lease, err := p.Acquire(ctx, c, blocked)
		if err != nil {
			if err == ErrNoProxy && len(blocked) > 0 {
				// Every remaining IP already blocked this request.
				return res, ErrParked
			}
			return res, err
		}
		res.Attempts++
		px := lease.Proxy()
		res.FinalProxy = px.ID()
		out, ferr := fn(ctx, px)
		lease.Release(out)

		switch out {
		case OK:
			return res, nil
		case Fatal:
			if ferr == nil {
				ferr = fmt.Errorf("request failed")
			}
			return res, ferr
		case Blocked:
			blocked[px.ID()] = true
			res.BlockedOn = append(res.BlockedOn, px.ID())
			if len(blocked) >= p.cfg.ParkAfter {
				return res, ErrParked
			}
			if err := p.sleep(ctx, p.randBetween(p.cfg.FailoverDelayMin, p.cfg.FailoverDelayMax)); err != nil {
				return res, err
			}
		case ProxyFailed:
			res.ProxyFailures++
			if res.ProxyFailures > p.cfg.MaxProxyFailures {
				return res, fmt.Errorf("too many proxy failures: %w", errOrGeneric(ferr))
			}
		case Transient:
			res.Transient++
			if res.Transient >= p.cfg.TransientAttempts {
				return res, fmt.Errorf("gave up after %d transient errors: %w", res.Transient, errOrGeneric(ferr))
			}
			backoff := p.cfg.TransientBackoff << (res.Transient - 1)
			if err := p.sleep(ctx, p.jittered(backoff)); err != nil {
				return res, err
			}
		default:
			return res, fmt.Errorf("unknown outcome %d", out)
		}
	}
}

func errOrGeneric(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("no error detail")
}

func (p *Pool) randBetween(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return lo + time.Duration(p.rng.Int63n(int64(hi-lo)))
}

// jittered returns d ± 25%.
func (p *Pool) jittered(d time.Duration) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Duration(float64(d) * (0.75 + 0.5*p.rng.Float64()))
}

func (p *Pool) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
