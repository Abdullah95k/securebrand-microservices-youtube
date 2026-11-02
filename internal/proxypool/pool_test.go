package proxypool

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// noJitter keeps intervals exact in tests (zero would mean "use default").
const noJitter = 1e-12

func makeProxies(t *testing.T, hosts ...string) []*Proxy {
	t.Helper()
	var lines []string
	for _, h := range hosts {
		lines = append(lines, proxyLine(h))
	}
	ps, err := ParseList(lines)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// take simulates Acquire at a fixed time.
func take(t *testing.T, p *Pool, now time.Time, c Class) *proxyState {
	t.Helper()
	st, wait := p.pickAt(now, c, nil, "")
	if st == nil {
		t.Fatalf("no proxy ready at %v (wait until %v)", now.Sub(t0), wait)
	}
	st.inFlight = true
	st.requests++
	p.subnetLastUsed[st.p.subnet] = now
	return st
}

func TestPacingPerIP(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80", "198.51.100.1:80"), Config{MinInterval: 2 * time.Second, Jitter: noJitter}, 1)
	a := take(t, p, t0, ClassBrowse)
	b := take(t, p, t0, ClassBrowse)
	if a == b {
		t.Fatal("an IP must handle one request at a time")
	}
	if st, _ := p.pickAt(t0, ClassBrowse, nil, ""); st != nil {
		t.Fatal("both IPs are busy; nothing should be ready")
	}
	p.releaseAt(t0, a, ClassBrowse, OK)
	p.releaseAt(t0, b, ClassBrowse, OK)
	st, wait := p.pickAt(t0.Add(time.Second), ClassBrowse, nil, "")
	if st != nil {
		t.Fatal("IPs must wait MinInterval between requests")
	}
	if got := wait.Sub(t0); got < 2*time.Second-time.Millisecond || got > 2*time.Second+time.Millisecond {
		t.Errorf("wait = %v, want ≈2s", got)
	}
	take(t, p, t0.Add(2*time.Second), ClassBrowse)
}

func TestPrefersLeastRecentlyUsedSubnet(t *testing.T) {
	// Two IPs in one /24, one IP in another network.
	p := New(makeProxies(t, "192.0.2.1:80", "192.0.2.2:80", "198.51.100.1:80"), Config{Jitter: noJitter}, 1)
	first := take(t, p, t0, ClassBrowse)
	second := take(t, p, t0.Add(time.Millisecond), ClassBrowse)
	if first.p.subnet == second.p.subnet {
		t.Error("the second request should go to the other subnet")
	}
}

func TestCooldownScheduleAndStrikeReset(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80"), Config{Jitter: noJitter, QuarantineDays: 100, DisableBrake: true}, 1)
	now := t0
	want := []time.Duration{30 * time.Minute, 2 * time.Hour, 12 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, w := range want {
		st := take(t, p, now, ClassSearch)
		p.releaseAt(now, st, ClassSearch, Blocked)
		cs := st.classes[ClassSearch]
		if got := cs.coolUntil.Sub(now); got != w {
			t.Fatalf("block %d: cooldown %v, want %v", i+1, got, w)
		}
		if s, _ := p.pickAt(now.Add(w-time.Second), ClassSearch, nil, ""); s != nil {
			t.Fatalf("block %d: IP available before its cooldown ended", i+1)
		}
		now = cs.coolUntil
	}
	// A clean day resets the strike count: the next block cools for 30 min.
	now = now.Add(24 * time.Hour)
	st := take(t, p, now, ClassSearch)
	p.releaseAt(now, st, ClassSearch, Blocked)
	if got := st.classes[ClassSearch].coolUntil.Sub(now); got != 30*time.Minute {
		t.Errorf("after a clean day cooldown = %v, want 30m", got)
	}
}

func TestHealthIsPerEndpointClass(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80"), Config{Jitter: noJitter, DisableBrake: true}, 1)
	st := take(t, p, t0, ClassSearch)
	p.releaseAt(t0, st, ClassSearch, Blocked)
	later := t0.Add(10 * time.Second)
	if s, _ := p.pickAt(later, ClassSearch, nil, ""); s != nil {
		t.Error("blocked for search; must not serve search")
	}
	if s, _ := p.pickAt(later, ClassBrowse, nil, ""); s == nil {
		t.Error("blocked for search only; should still serve browse")
	}
}

func TestQuarantineAfterBlocksOnThreeDays(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "198.51.100.1:80")
	p := New(ps, Config{Jitter: noJitter, BrakePauseFraction: 0.99, BrakeSlowFraction: 0.99}, 1)
	for day := 0; day < 3; day++ {
		now := t0.Add(time.Duration(day) * 24 * time.Hour)
		st := p.byID[ps[0].ID()]
		if st.quarantined {
			t.Fatalf("quarantined too early, day %d", day)
		}
		st.inFlight = true
		p.releaseAt(now, st, ClassBrowse, Blocked)
	}
	if !p.byID[ps[0].ID()].quarantined {
		t.Fatal("want quarantine after blocks on 3 distinct days")
	}
	if s, _ := p.pickAt(t0.Add(30*24*time.Hour), ClassBrowse, map[string]bool{ps[1].ID(): true}, ""); s != nil {
		t.Error("a quarantined IP must not be used")
	}
	if !p.Unquarantine(ps[0].ID()) || p.byID[ps[0].ID()].quarantined {
		t.Error("Unquarantine should restore the IP")
	}
}

func TestSubnetCooldown(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "192.0.2.2:80", "192.0.2.3:80", "192.0.2.4:80", "198.51.100.1:80")
	p := New(ps, Config{Jitter: noJitter, BrakePauseFraction: 0.99, BrakeSlowFraction: 0.99}, 1)
	for _, px := range ps[:3] {
		st := p.byID[px.ID()]
		st.inFlight = true
		p.releaseAt(t0, st, ClassSearch, Blocked)
	}
	later := t0.Add(time.Minute)
	exclude := map[string]bool{ps[4].ID(): true}
	if s, _ := p.pickAt(later, ClassSearch, exclude, ""); s != nil {
		t.Errorf("192.0.2.4 shares the blocked subnet and should be cooling, got %s", s.p.id)
	}
	if s, _ := p.pickAt(later, ClassSearch, nil, ""); s == nil || s.p.id != ps[4].ID() {
		t.Error("the IP in another network should still serve search")
	}
	if s, _ := p.pickAt(later, ClassBrowse, exclude, ""); s == nil {
		t.Error("subnet cooldown is per class; browse should still work")
	}
	p.now = func() time.Time { return later }
	if got := p.Stats().SubnetCooldowns; got != 1 {
		t.Errorf("Stats().SubnetCooldowns = %d, want 1", got)
	}
}

func TestBrakeSlowsAndRecovers(t *testing.T) {
	var hosts []string
	for i := 1; i <= 10; i++ {
		hosts = append(hosts, fmt.Sprintf("203.0.%d.1:80", 113+i))
	}
	p := New(makeProxies(t, hosts...), Config{MinInterval: time.Second, Jitter: noJitter}, 1)
	// 3 of 10 IPs blocked (30% > 20%) → slow down, but no pause (≤ 50%).
	for i := 0; i < 3; i++ {
		st := p.states[i]
		st.inFlight = true
		p.releaseAt(t0, st, ClassBrowse, Blocked)
	}
	if p.speed >= 1 {
		t.Fatalf("speed = %v, want < 1 after 30%% of IPs blocked", p.speed)
	}
	if !p.pausedUntil.IsZero() {
		t.Fatal("30% blocked should slow down, not pause")
	}
	slowed := p.speed
	st := take(t, p, t0, ClassBrowse)
	p.releaseAt(t0, st, ClassBrowse, OK)
	if gap := st.nextAllowed.Sub(t0); gap <= time.Second {
		t.Errorf("interval %v should be longer than 1s while slowed", gap)
	}
	// Speed comes back by 0.05 per clean 10 minutes.
	p.recoverAt(t0.Add(30 * time.Minute))
	if want := slowed + 0.15; p.speed < want-1e-9 || p.speed > want+1e-9 {
		t.Errorf("speed after 30 clean minutes = %v, want %v", p.speed, want)
	}
	p.recoverAt(t0.Add(48 * time.Hour))
	if p.speed != 1 {
		t.Errorf("speed should recover fully, got %v", p.speed)
	}
}

func TestBrakePausesWhenMostIPsBlocked(t *testing.T) {
	var hosts []string
	for i := 1; i <= 10; i++ {
		hosts = append(hosts, fmt.Sprintf("203.0.%d.1:80", 113+i))
	}
	p := New(makeProxies(t, hosts...), Config{Jitter: noJitter}, 1)
	for i := 0; i < 6; i++ {
		st := p.states[i]
		st.inFlight = true
		p.releaseAt(t0, st, ClassBrowse, Blocked)
	}
	pause := p.pausedUntil.Sub(t0)
	if pause < 30*time.Minute || pause > 60*time.Minute {
		t.Fatalf("pause = %v, want 30–60 min", pause)
	}
	st, wait := p.pickAt(t0.Add(time.Minute), ClassRSS, nil, "")
	if st != nil || !wait.Equal(p.pausedUntil) {
		t.Error("while paused nothing may be sent, on any endpoint")
	}
	if s, _ := p.pickAt(p.pausedUntil, ClassRSS, nil, ""); s == nil {
		t.Error("work resumes after the pause")
	}
	p.now = func() time.Time { return t0 }
	if s := p.Stats(); s.Pauses != 1 || s.PausedTotal != pause {
		t.Errorf("Stats: %d pauses for %v; want 1 for %v", s.Pauses, s.PausedTotal, pause)
	}
}

func TestProxyFailureRestsBriefly(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80"), Config{Jitter: noJitter, MinInterval: time.Second}, 1)
	st := take(t, p, t0, ClassBrowse)
	p.releaseAt(t0, st, ClassBrowse, ProxyFailed)
	if s, _ := p.pickAt(t0.Add(4*time.Minute), ClassBrowse, nil, ""); s != nil {
		t.Error("a failed proxy rests for ProxyErrorCooldown")
	}
	if s, _ := p.pickAt(t0.Add(5*time.Minute), ClassBrowse, nil, ""); s == nil {
		t.Error("the proxy should be back after its rest")
	}
	if st.blocks != 0 {
		t.Error("a proxy failure is not a YouTube block")
	}
}

func TestAcquireNoProxyAndCancel(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80")
	p := New(ps, Config{Jitter: noJitter}, 1)
	if _, err := p.Acquire(context.Background(), ClassBrowse, map[string]bool{ps[0].ID(): true}); !errors.Is(err, ErrNoProxy) {
		t.Errorf("err = %v, want ErrNoProxy", err)
	}
	l, err := p.Acquire(context.Background(), ClassBrowse, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(ctx, ClassBrowse, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded while the only IP is busy", err)
	}
	l.Release(OK)
	l.Release(Blocked) // second release is ignored
	if p.Stats().Proxies[0].Blocks != 0 {
		t.Error("a lease must only be released once")
	}
}

func TestStats(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80")
	p := New(ps, Config{Jitter: noJitter}, 1)
	st := take(t, p, t0, ClassSearch)
	p.now = func() time.Time { return t0 }
	p.releaseAt(t0, st, ClassSearch, Blocked)
	s := p.Stats()
	if len(s.Proxies) != 1 || s.Proxies[0].Requests != 1 || s.Proxies[0].Blocks != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if _, ok := s.Proxies[0].CoolUntil[ClassSearch]; !ok {
		t.Error("stats should show the search cooldown")
	}
	if got := s.Proxies[0].BlocksByClass; got[ClassSearch] != 1 || len(got) != 1 {
		t.Errorf("BlocksByClass = %v, want search: 1", got)
	}
}

func TestDisableBrake(t *testing.T) {
	p := New(makeProxies(t, "192.0.2.1:80"), Config{Jitter: noJitter, DisableBrake: true}, 1)
	st := take(t, p, t0, ClassSearch)
	p.releaseAt(t0, st, ClassSearch, Blocked)
	if p.speed != 1 || !p.pausedUntil.IsZero() {
		t.Errorf("brake disabled, but speed=%v pausedUntil=%v", p.speed, p.pausedUntil)
	}
}

func TestSingleIPBlockPausesWithBrake(t *testing.T) {
	// With the brake on, one blocked IP out of one is 100% of the pool.
	p := New(makeProxies(t, "192.0.2.1:80"), Config{Jitter: noJitter}, 1)
	st := take(t, p, t0, ClassSearch)
	p.releaseAt(t0, st, ClassSearch, Blocked)
	if p.pausedUntil.IsZero() {
		t.Error("want a pause when every IP in the pool is blocked")
	}
}

func TestPreferKeepsAChainOnOneIP(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "198.51.100.1:80")
	p := New(ps, Config{MinInterval: 2 * time.Second, Jitter: noJitter, DisableBrake: true}, 1)
	b := ps[1].ID()

	// Ready: the preferred IP is used even though the other is idle longer.
	st, _ := p.pickAt(t0, ClassBrowse, nil, b)
	if st == nil || st.p.id != b {
		t.Fatalf("want the preferred proxy, got %v", st)
	}
	st.inFlight = true
	p.releaseAt(t0, st, ClassBrowse, OK)

	// Pacing: wait for the preferred IP's next slot instead of switching.
	st, wait := p.pickAt(t0.Add(time.Second), ClassBrowse, nil, b)
	if st != nil || !wait.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("want to wait for the preferred IP until +2s, got %v wait %v", st, wait.Sub(t0))
	}

	// Cooling down: fall back to another IP.
	bs := p.byID[b]
	bs.inFlight = true
	p.releaseAt(t0.Add(2*time.Second), bs, ClassBrowse, Blocked)
	st, _ = p.pickAt(t0.Add(3*time.Second), ClassBrowse, nil, b)
	if st == nil || st.p.id == b {
		t.Fatalf("a cooling preferred IP must not be used; got %v", st)
	}
}

func TestDoPreferStartsOnPreferredAndFailsOver(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "198.51.100.1:80", "203.0.113.1:80")
	p := New(ps, fastConfig(), 1)
	want := ps[2].ID()
	var used []string
	_, err := p.DoPrefer(context.Background(), ClassNext, want, func(_ context.Context, px *Proxy) (Outcome, error) {
		used = append(used, px.ID())
		if len(used) == 1 {
			return Blocked, nil
		}
		return OK, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if used[0] != want || used[1] == want {
		t.Errorf("want first attempt on the preferred IP and the retry elsewhere, used %v", used)
	}
}

// TestPlayerBlocksDoNotBrake: blocks on the player endpoint rest that IP's
// player class but never slow or pause the pool; the same blocks on browse
// do. An empty BrakeIgnore brakes on player too.
func TestPlayerBlocksDoNotBrake(t *testing.T) {
	var hosts []string
	for i := 1; i <= 10; i++ {
		hosts = append(hosts, fmt.Sprintf("203.0.%d.1:80", 113+i))
	}
	blockSix := func(p *Pool, c Class) {
		for i := 0; i < 6; i++ {
			st := p.states[i]
			st.inFlight = true
			p.releaseAt(t0, st, c, Blocked)
		}
	}
	p := New(makeProxies(t, hosts...), Config{Jitter: noJitter}, 1)
	blockSix(p, ClassPlayer)
	if p.speed != 1 || !p.pausedUntil.IsZero() {
		t.Fatalf("after 6 player blocks: speed %v, paused until %v; want full speed, no pause", p.speed, p.pausedUntil)
	}
	if st, _ := p.pickAt(t0.Add(time.Minute), ClassPlayer, map[string]bool{p.states[6].p.id: true, p.states[7].p.id: true, p.states[8].p.id: true, p.states[9].p.id: true}, ""); st != nil {
		t.Error("the blocked IPs' player class should still be resting")
	}
	if st, _ := p.pickAt(t0.Add(time.Minute), ClassBrowse, nil, ""); st == nil {
		t.Error("browse should carry on")
	}

	strict := New(makeProxies(t, hosts...), Config{Jitter: noJitter, BrakeIgnore: []Class{}}, 1)
	blockSix(strict, ClassPlayer)
	if strict.pausedUntil.IsZero() {
		t.Error("with an empty BrakeIgnore, player blocks should brake like any other")
	}
}

// TestFailoverAndRecoveryCounters covers the E2 counters: a request blocked
// on one IP that succeeds on another, a request parked after ParkAfter IPs,
// and whether an IP works again after its rest.
func TestFailoverAndRecoveryCounters(t *testing.T) {
	ps := makeProxies(t, "192.0.2.1:80", "198.51.100.1:80", "203.0.113.1:80")
	p := New(ps, Config{MinInterval: time.Millisecond, Jitter: noJitter, DisableBrake: true,
		FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond}, 1)
	first := ""
	_, err := p.Do(context.Background(), ClassBrowse, func(_ context.Context, px *Proxy) (Outcome, error) {
		if first == "" {
			first = px.ID()
			return Blocked, nil
		}
		return OK, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Do(context.Background(), ClassNext, func(context.Context, *Proxy) (Outcome, error) { return Blocked, nil })
	if !errors.Is(err, ErrParked) {
		t.Fatalf("err = %v, want ErrParked", err)
	}
	s := p.Stats()
	if b := s.Classes[ClassBrowse]; b.BlockedRequests != 1 || b.FailoverOK != 1 || b.Parked != 0 {
		t.Errorf("browse counters %+v; want 1 blocked request that succeeded elsewhere", b)
	}
	if n := s.Classes[ClassNext]; n.BlockedRequests != 1 || n.Parked != 1 || n.FailoverOK != 0 {
		t.Errorf("next counters %+v; want 1 parked request", n)
	}

	// Recovery: the first outcome after a block says whether the rest worked.
	q := New(ps[:1], Config{Jitter: noJitter, DisableBrake: true}, 1)
	st := take(t, q, t0, ClassSearch)
	q.releaseAt(t0, st, ClassSearch, Blocked)
	afterRest := t0.Add(31 * time.Minute)
	st = take(t, q, afterRest, ClassSearch)
	q.releaseAt(afterRest, st, ClassSearch, OK)
	st = take(t, q, afterRest.Add(time.Minute), ClassSearch)
	q.releaseAt(afterRest.Add(time.Minute), st, ClassSearch, Blocked) // a new block, not a re-block
	again := afterRest.Add(time.Minute + 2*time.Hour + time.Minute)   // second strike: 2 h rest
	st = take(t, q, again, ClassSearch)
	q.releaseAt(again, st, ClassSearch, Blocked) // blocked on the first request after the rest
	q.now = func() time.Time { return again }
	c := q.Stats().Classes[ClassSearch]
	if c.Recovered != 1 || c.Reblocked != 1 || len(c.RecoverySeconds) != 1 || c.RecoverySeconds[0] != (31*time.Minute).Seconds() {
		t.Errorf("search counters %+v; want 1 recovery after 31 min and 1 re-block", c)
	}
}
