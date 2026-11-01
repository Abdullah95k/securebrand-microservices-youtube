package proxypool

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Class groups requests by YouTube endpoint, because an IP can be blocked
// for one endpoint (for example search) and still work for others.
type Class string

// Endpoint classes.
const (
	ClassSearch Class = "search"
	ClassBrowse Class = "browse"
	ClassNext   Class = "next"
	ClassPlayer Class = "player"
	ClassWatch  Class = "watch"
	ClassRSS    Class = "rss"
	ClassCDN    Class = "cdn"
)

// Outcome is what happened to a request sent through a lease.
type Outcome int

// Outcomes.
const (
	// OK: the request succeeded.
	OK Outcome = iota
	// Blocked: YouTube answered with a block signal (429/403, a bot check,
	// the "sorry" page). The IP cools down for this class.
	Blocked
	// ProxyFailed: the proxy itself failed (connection refused, 407…). The IP
	// rests briefly; this is not a YouTube block.
	ProxyFailed
	// Transient: a temporary error such as a 5xx or a timeout from YouTube.
	Transient
	// Fatal: an error that retrying won't fix (bad request, parse error).
	Fatal
)

// Errors returned by Acquire and Do.
var (
	// ErrNoProxy: no proxy can serve this class (all quarantined or excluded).
	ErrNoProxy = errors.New("proxypool: no proxy available for this request")
	// ErrParked: the request was blocked on too many different IPs; the
	// caller should retry it much later (the plan says 6 hours).
	ErrParked = errors.New("proxypool: request blocked on several IPs; park it and retry later")
)

// Config holds the pacing and failover settings. Zero values take the
// defaults from DefaultConfig.
type Config struct {
	MinInterval time.Duration // per IP, between requests (default 2 s)
	Jitter      float64       // ± fraction applied to MinInterval (default 0.3)

	Cooldowns   []time.Duration // after the 1st, 2nd, 3rd, 4th+ block (default 30m, 2h, 12h, 24h)
	StrikeReset time.Duration   // clean time after a cooldown ends that resets the strike count (default 24h)

	QuarantineDays   int           // distinct days with blocks that quarantine an IP (default 3)
	QuarantineWindow time.Duration // window for counting those days (default 7 days)

	SubnetThreshold int           // IPs of one subnet cooling for a class that cool the subnet (default 3)
	SubnetCooldown  time.Duration // default 2h

	ProxyErrorCooldown time.Duration // rest after a proxy failure (default 5 min)

	FailoverDelayMin time.Duration // wait before retrying a blocked request elsewhere (default 5 s)
	FailoverDelayMax time.Duration // default 60 s
	ParkAfter        int           // distinct IPs that blocked one request before parking it (default 3)

	TransientAttempts int           // attempts for transient errors (default 5)
	TransientBackoff  time.Duration // first backoff, doubled each time (default 2 s)
	MaxProxyFailures  int           // proxy failures tolerated per request (default 3)

	// DisableBrake turns the pool brake off. Use it for single-IP pools, such
	// as the Phase 0 per-IP measurements, where one block would otherwise
	// count as "100% of IPs blocked".
	DisableBrake       bool
	BrakeWindow        time.Duration // window for counting blocked IPs (default 1h)
	BrakeSlowFraction  float64       // above this share of blocked IPs, slow down (default 0.2)
	BrakePauseFraction float64       // above this share, pause everything (default 0.5)
	BrakeSlowFactor    float64       // speed multiplier applied per block while slowed (default 0.7)
	BrakeMinSpeed      float64       // lowest speed multiplier (default 0.1)
	BrakeRecoverStep   float64       // speed regained per clean period (default 0.05)
	BrakeRecoverEvery  time.Duration // clean period (default 10 min)
	BrakePauseMin      time.Duration // default 30 min
	BrakePauseMax      time.Duration // default 60 min
}

// DefaultConfig returns the plan's default settings.
func DefaultConfig() Config {
	return Config{
		MinInterval:        2 * time.Second,
		Jitter:             0.3,
		Cooldowns:          []time.Duration{30 * time.Minute, 2 * time.Hour, 12 * time.Hour, 24 * time.Hour},
		StrikeReset:        24 * time.Hour,
		QuarantineDays:     3,
		QuarantineWindow:   7 * 24 * time.Hour,
		SubnetThreshold:    3,
		SubnetCooldown:     2 * time.Hour,
		ProxyErrorCooldown: 5 * time.Minute,
		FailoverDelayMin:   5 * time.Second,
		FailoverDelayMax:   60 * time.Second,
		ParkAfter:          3,
		TransientAttempts:  5,
		TransientBackoff:   2 * time.Second,
		MaxProxyFailures:   3,
		BrakeWindow:        time.Hour,
		BrakeSlowFraction:  0.2,
		BrakePauseFraction: 0.5,
		BrakeSlowFactor:    0.7,
		BrakeMinSpeed:      0.1,
		BrakeRecoverStep:   0.05,
		BrakeRecoverEvery:  10 * time.Minute,
		BrakePauseMin:      30 * time.Minute,
		BrakePauseMax:      60 * time.Minute,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	setDur := func(v *time.Duration, def time.Duration) {
		if *v == 0 {
			*v = def
		}
	}
	setDur(&c.MinInterval, d.MinInterval)
	if c.Jitter == 0 {
		c.Jitter = d.Jitter
	}
	if len(c.Cooldowns) == 0 {
		c.Cooldowns = d.Cooldowns
	}
	setDur(&c.StrikeReset, d.StrikeReset)
	if c.QuarantineDays == 0 {
		c.QuarantineDays = d.QuarantineDays
	}
	setDur(&c.QuarantineWindow, d.QuarantineWindow)
	if c.SubnetThreshold == 0 {
		c.SubnetThreshold = d.SubnetThreshold
	}
	setDur(&c.SubnetCooldown, d.SubnetCooldown)
	setDur(&c.ProxyErrorCooldown, d.ProxyErrorCooldown)
	setDur(&c.FailoverDelayMin, d.FailoverDelayMin)
	setDur(&c.FailoverDelayMax, d.FailoverDelayMax)
	if c.ParkAfter == 0 {
		c.ParkAfter = d.ParkAfter
	}
	if c.TransientAttempts == 0 {
		c.TransientAttempts = d.TransientAttempts
	}
	setDur(&c.TransientBackoff, d.TransientBackoff)
	if c.MaxProxyFailures == 0 {
		c.MaxProxyFailures = d.MaxProxyFailures
	}
	setDur(&c.BrakeWindow, d.BrakeWindow)
	if c.BrakeSlowFraction == 0 {
		c.BrakeSlowFraction = d.BrakeSlowFraction
	}
	if c.BrakePauseFraction == 0 {
		c.BrakePauseFraction = d.BrakePauseFraction
	}
	if c.BrakeSlowFactor == 0 {
		c.BrakeSlowFactor = d.BrakeSlowFactor
	}
	if c.BrakeMinSpeed == 0 {
		c.BrakeMinSpeed = d.BrakeMinSpeed
	}
	if c.BrakeRecoverStep == 0 {
		c.BrakeRecoverStep = d.BrakeRecoverStep
	}
	setDur(&c.BrakeRecoverEvery, d.BrakeRecoverEvery)
	setDur(&c.BrakePauseMin, d.BrakePauseMin)
	setDur(&c.BrakePauseMax, d.BrakePauseMax)
	return c
}

type classState struct {
	strikes   int
	coolUntil time.Time
	lastBlock time.Time
}

type proxyState struct {
	p             *Proxy
	nextAllowed   time.Time
	inFlight      bool
	classes       map[Class]*classState
	proxyErrUntil time.Time
	blockDays     map[string]time.Time // "2006-01-02" → last block that day
	quarantined   bool

	requests, blocks, proxyErrors int64
}

func (s *proxyState) class(c Class) *classState {
	cs, ok := s.classes[c]
	if !ok {
		cs = &classState{}
		s.classes[c] = cs
	}
	return cs
}

type blockEvent struct {
	at    time.Time
	proxy string
}

// Pool hands out proxies one request at a time. It is safe for concurrent use.
type Pool struct {
	cfg Config
	now func() time.Time

	mu             sync.Mutex
	rng            *rand.Rand
	states         []*proxyState
	byID           map[string]*proxyState
	subnetCool     map[string]map[Class]time.Time
	subnetLastUsed map[string]time.Time
	events         []blockEvent
	speed          float64
	pausedUntil    time.Time
	lastBlockAt    time.Time
	lastRecoverAt  time.Time
}

// New creates a pool. seed makes jitter reproducible in tests; use
// time.Now().UnixNano() in production.
func New(proxies []*Proxy, cfg Config, seed int64) *Pool {
	p := &Pool{
		cfg:            cfg.withDefaults(),
		now:            time.Now,
		rng:            rand.New(rand.NewSource(seed)),
		byID:           map[string]*proxyState{},
		subnetCool:     map[string]map[Class]time.Time{},
		subnetLastUsed: map[string]time.Time{},
		speed:          1,
	}
	for _, px := range proxies {
		st := &proxyState{p: px, classes: map[Class]*classState{}, blockDays: map[string]time.Time{}}
		p.states = append(p.states, st)
		p.byID[px.id] = st
	}
	return p
}

// Lease is permission to send one request through a proxy. Call Release
// exactly once with the outcome.
type Lease struct {
	pool  *Pool
	st    *proxyState
	class Class
	once  sync.Once
}

// Proxy returns the leased proxy.
func (l *Lease) Proxy() *Proxy { return l.st.p }

// Release returns the proxy to the pool and records the outcome.
func (l *Lease) Release(o Outcome) {
	l.once.Do(func() {
		l.pool.mu.Lock()
		defer l.pool.mu.Unlock()
		l.pool.releaseAt(l.pool.now(), l.st, l.class, o)
	})
}

// Acquire waits until a proxy may send a request of class c, skipping the
// proxy IDs in exclude. It returns ErrNoProxy if no proxy can ever serve it.
func (p *Pool) Acquire(ctx context.Context, c Class, exclude map[string]bool) (*Lease, error) {
	for {
		p.mu.Lock()
		now := p.now()
		st, wait := p.pickAt(now, c, exclude)
		if st != nil {
			st.inFlight = true
			st.requests++
			p.subnetLastUsed[st.p.subnet] = now
			p.mu.Unlock()
			return &Lease{pool: p, st: st, class: c}, nil
		}
		p.mu.Unlock()
		if wait.IsZero() {
			return nil, ErrNoProxy
		}
		// Re-check at least every 250 ms: a release may free a proxy sooner.
		d := wait.Sub(now)
		if d > 250*time.Millisecond {
			d = 250 * time.Millisecond
		}
		if d < time.Millisecond {
			d = time.Millisecond
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// pickAt chooses a ready proxy, or returns the time worth waiting until.
// A zero time means no proxy can serve class c. Caller holds p.mu.
func (p *Pool) pickAt(now time.Time, c Class, exclude map[string]bool) (*proxyState, time.Time) {
	p.recoverAt(now)
	eligible := false
	for _, st := range p.states {
		if !st.quarantined && !exclude[st.p.id] {
			eligible = true
			break
		}
	}
	if !eligible {
		return nil, time.Time{}
	}
	if now.Before(p.pausedUntil) {
		return nil, p.pausedUntil
	}
	var ready []*proxyState
	var earliest time.Time
	note := func(t time.Time) {
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	for _, st := range p.states {
		if st.quarantined || exclude[st.p.id] {
			continue
		}
		if st.inFlight {
			note(now.Add(50 * time.Millisecond))
			continue
		}
		cs := st.class(c)
		if cs.strikes > 0 && now.Sub(cs.coolUntil) >= p.cfg.StrikeReset {
			cs.strikes = 0 // a clean day after the cooldown ended
		}
		availableAt := maxTime(st.nextAllowed, cs.coolUntil, st.proxyErrUntil, p.subnetCool[st.p.subnet][c])
		if availableAt.After(now) {
			note(availableAt)
			continue
		}
		ready = append(ready, st)
	}
	if len(ready) == 0 {
		return nil, earliest
	}
	// Prefer the subnet used least recently, then the IP idle longest.
	sort.SliceStable(ready, func(i, j int) bool {
		si, sj := p.subnetLastUsed[ready[i].p.subnet], p.subnetLastUsed[ready[j].p.subnet]
		if !si.Equal(sj) {
			return si.Before(sj)
		}
		return ready[i].nextAllowed.Before(ready[j].nextAllowed)
	})
	return ready[0], time.Time{}
}

// releaseAt records an outcome. Caller holds p.mu.
func (p *Pool) releaseAt(now time.Time, st *proxyState, c Class, o Outcome) {
	st.inFlight = false
	st.nextAllowed = now.Add(p.intervalLocked())

	switch o {
	case Blocked:
		cs := st.class(c)
		if cs.strikes > 0 && now.Sub(cs.coolUntil) >= p.cfg.StrikeReset {
			cs.strikes = 0 // a clean day after the cooldown ended
		}
		cs.strikes++
		idx := cs.strikes - 1
		if idx >= len(p.cfg.Cooldowns) {
			idx = len(p.cfg.Cooldowns) - 1
		}
		cs.coolUntil = now.Add(p.cfg.Cooldowns[idx])
		cs.lastBlock = now
		st.blocks++

		// Quarantine after blocks on several distinct days.
		st.blockDays[now.UTC().Format("2006-01-02")] = now
		for day, at := range st.blockDays {
			if now.Sub(at) > p.cfg.QuarantineWindow {
				delete(st.blockDays, day)
			}
		}
		if len(st.blockDays) >= p.cfg.QuarantineDays {
			st.quarantined = true
		}

		p.coolSubnetLocked(now, st.p.subnet, c)
		p.brakeLocked(now, st.p.id)

	case ProxyFailed:
		st.proxyErrors++
		st.proxyErrUntil = now.Add(p.cfg.ProxyErrorCooldown)
	}
}

// intervalLocked is the per-IP gap, slowed by the brake, with jitter.
func (p *Pool) intervalLocked() time.Duration {
	base := float64(p.cfg.MinInterval) / p.speed
	j := 1 + p.cfg.Jitter*(2*p.rng.Float64()-1)
	return time.Duration(base * j)
}

func (p *Pool) coolSubnetLocked(now time.Time, subnet string, c Class) {
	if subnet == "direct" {
		return
	}
	cooling := 0
	for _, st := range p.states {
		if st.p.subnet != subnet {
			continue
		}
		if cs, ok := st.classes[c]; ok && cs.coolUntil.After(now) {
			cooling++
		}
	}
	if cooling >= p.cfg.SubnetThreshold {
		m := p.subnetCool[subnet]
		if m == nil {
			m = map[Class]time.Time{}
			p.subnetCool[subnet] = m
		}
		until := now.Add(p.cfg.SubnetCooldown)
		if until.After(m[c]) {
			m[c] = until
		}
	}
}

// brakeLocked applies the pool brake after a block. Caller holds p.mu.
func (p *Pool) brakeLocked(now time.Time, proxyID string) {
	if p.cfg.DisableBrake {
		return
	}
	p.lastBlockAt = now
	p.events = append(p.events, blockEvent{at: now, proxy: proxyID})
	cut := 0
	for cut < len(p.events) && now.Sub(p.events[cut].at) > p.cfg.BrakeWindow {
		cut++
	}
	p.events = p.events[cut:]

	blockedIPs := map[string]bool{}
	for _, e := range p.events {
		blockedIPs[e.proxy] = true
	}
	active := 0
	for _, st := range p.states {
		if !st.quarantined || blockedIPs[st.p.id] {
			active++
		}
	}
	if active == 0 {
		return
	}
	share := float64(len(blockedIPs)) / float64(active)
	switch {
	case share > p.cfg.BrakePauseFraction:
		if !now.Before(p.pausedUntil) {
			span := p.cfg.BrakePauseMax - p.cfg.BrakePauseMin
			d := p.cfg.BrakePauseMin
			if span > 0 {
				d += time.Duration(p.rng.Int63n(int64(span)))
			}
			p.pausedUntil = now.Add(d)
		}
		p.slowLocked()
	case share > p.cfg.BrakeSlowFraction:
		p.slowLocked()
	}
}

func (p *Pool) slowLocked() {
	p.speed *= p.cfg.BrakeSlowFactor
	if p.speed < p.cfg.BrakeMinSpeed {
		p.speed = p.cfg.BrakeMinSpeed
	}
}

// recoverAt regains speed after clean periods. Caller holds p.mu.
func (p *Pool) recoverAt(now time.Time) {
	if p.speed >= 1 {
		return
	}
	from := p.lastBlockAt
	if p.lastRecoverAt.After(from) {
		from = p.lastRecoverAt
	}
	if now.Sub(p.lastBlockAt) < p.cfg.BrakeRecoverEvery {
		return
	}
	steps := int(now.Sub(from) / p.cfg.BrakeRecoverEvery)
	if steps <= 0 {
		return
	}
	p.speed += float64(steps) * p.cfg.BrakeRecoverStep
	if p.speed > 1 {
		p.speed = 1
	}
	p.lastRecoverAt = from.Add(time.Duration(steps) * p.cfg.BrakeRecoverEvery)
}

// Unquarantine puts a quarantined proxy back into service (an admin action,
// for example after the provider replaced the IP).
func (p *Pool) Unquarantine(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.byID[id]
	if !ok {
		return false
	}
	st.quarantined = false
	st.blockDays = map[string]time.Time{}
	return true
}

// ProxyStats describes one proxy, without any address or credential.
type ProxyStats struct {
	ID          string
	Kind        Kind
	Subnet      string
	Requests    int64
	Blocks      int64
	ProxyErrors int64
	Quarantined bool
	CoolUntil   map[Class]time.Time // only classes still cooling
}

// Stats is a snapshot of the pool for monitoring and the Phase 0 report.
type Stats struct {
	Proxies     []ProxyStats
	Speed       float64 // 1 = normal; lower when the brake is on
	PausedUntil time.Time
}

// Stats returns a snapshot.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s := Stats{Speed: p.speed}
	if now.Before(p.pausedUntil) {
		s.PausedUntil = p.pausedUntil
	}
	for _, st := range p.states {
		ps := ProxyStats{
			ID: st.p.id, Kind: st.p.kind, Subnet: st.p.subnet,
			Requests: st.requests, Blocks: st.blocks, ProxyErrors: st.proxyErrors,
			Quarantined: st.quarantined, CoolUntil: map[Class]time.Time{},
		}
		for c, cs := range st.classes {
			until := maxTime(cs.coolUntil, p.subnetCool[st.p.subnet][c])
			if until.After(now) {
				ps.CoolUntil[c] = until
			}
		}
		s.Proxies = append(s.Proxies, ps)
	}
	return s
}

func maxTime(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
}
