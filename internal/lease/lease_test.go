package lease_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/lease"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// One holder at a time; the other stands by and takes over once the
// holder gives the lease up; a renewal that finds the lease taken stops
// the work at once.
func TestLease(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	var running atomic.Int32 // processes doing the work now
	var maxRunning atomic.Int32
	var runs [2]atomic.Int32
	newLease := func(holder string) *lease.Lease {
		return &lease.Lease{Q: pool, Name: "test", Holder: holder, TTL: 600 * time.Millisecond, Renew: 100 * time.Millisecond, Log: quiet}
	}
	work := func(i int) func(context.Context) error {
		return func(ctx context.Context) error {
			runs[i].Add(1)
			n := running.Add(1)
			for {
				if m := maxRunning.Load(); n <= m || maxRunning.CompareAndSwap(m, n) {
					break
				}
			}
			<-ctx.Done()
			running.Add(-1)
			return nil
		}
	}
	actx, stopA := context.WithCancel(ctx)
	bctx, stopB := context.WithCancel(ctx)
	defer stopB()
	a, b := newLease("a"), newLease("b")
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- a.Run(actx, work(0)) }()
	waitFor(t, "a to take the lease", func() bool { return runs[0].Load() == 1 })
	go func() { doneB <- b.Run(bctx, work(1)) }()
	time.Sleep(time.Second) // b tries, finds it held, stands by; a keeps renewing past its TTL
	if runs[1].Load() != 0 || running.Load() != 1 {
		t.Fatalf("b ran %d times while a held the lease", runs[1].Load())
	}

	// a stops and gives the lease up: b takes it within a TTL.
	stopA()
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "b to take over", func() bool { return runs[1].Load() == 1 })

	// The lease is taken from b (as if b had stalled past its TTL and
	// another took it): b's next renewal stops its work.
	if _, err := pool.Exec(ctx, `UPDATE leases SET holder = 'thief', until = now() + interval '1 hour' WHERE name = 'test'`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "b to stop its work", func() bool { return running.Load() == 0 })
	if maxRunning.Load() != 1 {
		t.Errorf("%d processes worked at once", maxRunning.Load())
	}
	stopB()
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
	if runs[1].Load() != 1 {
		t.Errorf("b worked %d times; the thief still holds the lease", runs[1].Load())
	}
}

// Claim: free, held by another, expired, renewed by its holder.
func TestClaim(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	a := &lease.Lease{Q: pool, Name: "claim", Holder: "a", TTL: 300 * time.Millisecond}
	b := &lease.Lease{Q: pool, Name: "claim", Holder: "b", TTL: 300 * time.Millisecond}
	for _, step := range []struct {
		l    *lease.Lease
		want bool
		wait time.Duration
	}{{a, true, 0}, {b, false, 0}, {a, true, 0}, {b, true, 400 * time.Millisecond}, {a, false, 0}} {
		time.Sleep(step.wait)
		got, err := step.l.Claim(ctx)
		if err != nil || got != step.want {
			t.Fatalf("%s claims: %v (%v), want %v", step.l.Holder, got, err, step.want)
		}
	}
	if err := b.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Claim(ctx); !got {
		t.Error("a released lease must be free at once")
	}
	if lease.Holder() == lease.Holder() {
		t.Error("two holders' names must differ")
	}
}
