package proxypool

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fastConfig keeps real-time waits tiny so Do can be tested end to end.
func fastConfig() Config {
	return Config{
		MinInterval:        time.Millisecond,
		Jitter:             noJitter,
		FailoverDelayMin:   time.Millisecond,
		FailoverDelayMax:   2 * time.Millisecond,
		TransientBackoff:   time.Millisecond,
		ProxyErrorCooldown: time.Hour,
		BrakeSlowFraction:  0.99,
		BrakePauseFraction: 0.999,
	}
}

func TestDoFailsOverToAnotherIP(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "198.51.100.1:80", "203.0.113.1:80")
	p := New(ps, fastConfig(), 1)
	var used []string
	res, err := p.Do(context.Background(), ClassNext, func(_ context.Context, px *Proxy) (Outcome, error) {
		used = append(used, px.ID())
		if len(used) == 1 {
			return Blocked, errors.New("bot check")
		}
		return OK, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 2 || used[0] == used[1] {
		t.Fatalf("want a retry on a different IP, used %v", used)
	}
	if res.Attempts != 2 || len(res.BlockedOn) != 1 || res.FinalProxy != used[1] {
		t.Errorf("result = %+v", res)
	}
}

func TestDoParksAfterThreeDifferentIPs(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "198.51.100.1:80", "203.0.113.1:80", "203.0.114.1:80")
	p := New(ps, fastConfig(), 1)
	seen := map[string]bool{}
	res, err := p.Do(context.Background(), ClassSearch, func(_ context.Context, px *Proxy) (Outcome, error) {
		if seen[px.ID()] {
			t.Errorf("IP %s retried after it blocked this request", px.ID())
		}
		seen[px.ID()] = true
		return Blocked, nil
	})
	if !errors.Is(err, ErrParked) {
		t.Fatalf("err = %v, want ErrParked", err)
	}
	if len(res.BlockedOn) != 3 {
		t.Errorf("blocked on %d IPs, want 3", len(res.BlockedOn))
	}
}

func TestDoParksWhenEveryIPBlocked(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80", "198.51.100.1:80"), fastConfig(), 1)
	_, err := p.Do(context.Background(), ClassSearch, func(context.Context, *Proxy) (Outcome, error) {
		return Blocked, nil
	})
	if !errors.Is(err, ErrParked) {
		t.Fatalf("err = %v, want ErrParked when all IPs blocked", err)
	}
}

func TestDoTransientRetriesThenGivesUp(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80"), fastConfig(), 1)
	calls := 0
	reason := errors.New("http 503")
	res, err := p.Do(context.Background(), ClassBrowse, func(context.Context, *Proxy) (Outcome, error) {
		calls++
		return Transient, reason
	})
	if !errors.Is(err, reason) || calls != 5 || res.Transient != 5 {
		t.Fatalf("err=%v calls=%d res=%+v; want 5 attempts then the last reason", err, calls, res)
	}
}

func TestDoFatalReturnsImmediately(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80", "198.51.100.1:80"), fastConfig(), 1)
	calls := 0
	reason := errors.New("parse error")
	_, err := p.Do(context.Background(), ClassBrowse, func(context.Context, *Proxy) (Outcome, error) {
		calls++
		return Fatal, reason
	})
	if !errors.Is(err, reason) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoProxyFailureMovesOn(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "198.51.100.1:80")
	p := New(ps, fastConfig(), 1)
	var used []string
	_, err := p.Do(context.Background(), ClassBrowse, func(_ context.Context, px *Proxy) (Outcome, error) {
		used = append(used, px.ID())
		if len(used) == 1 {
			return ProxyFailed, errors.New("connection refused")
		}
		return OK, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 2 || used[0] == used[1] {
		t.Errorf("want the second attempt on another IP, used %v", used)
	}
}

func TestDoHonoursContext(t *testing.T) {
	cfg := fastConfig()
	cfg.FailoverDelayMin = time.Hour
	cfg.FailoverDelayMax = 2 * time.Hour
	p := New(makeProxies(t, "192.0.2.1:80", "198.51.100.1:80"), cfg, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Do(ctx, ClassBrowse, func(context.Context, *Proxy) (Outcome, error) { return Blocked, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded during the failover wait", err)
	}
}
