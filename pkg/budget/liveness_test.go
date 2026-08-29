package budget_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// beatingLease is a slot that counts its renewals.
type beatingLease struct {
	ttl   time.Duration
	beats atomic.Int64
}

func (l *beatingLease) Release(error)      {}
func (l *beatingLease) TTL() time.Duration { return l.ttl }
func (l *beatingLease) Heartbeat(context.Context) error {
	l.beats.Add(1)
	return nil
}

// forwardingLease is the shape somebody writes when they wrap a lease —
// logging one, tracing one, testing one. It forwards what the interface asks
// for and nothing else, because nothing else is asked for.
type forwardingLease struct{ inner budget.Lease }

func (l forwardingLease) Release(err error) { l.inner.Release(err) }

// These two are here because the compiler asked for them. That is the whole
// point: before liveness was part of Lease, this type compiled without them
// and renewed nothing.
func (l forwardingLease) TTL() time.Duration                  { return l.inner.TTL() }
func (l forwardingLease) Heartbeat(ctx context.Context) error { return l.inner.Heartbeat(ctx) }

// Liveness must be part of what a Lease is, not a second interface a caller
// remembers to look for.
//
// The distinction is not academic: today's other bug in this package was
// exactly this shape one level up. pkg/embed asks an embedder for its usage
// through an optional interface, budget's own decorator implemented only the
// required half, and a real run reported 258 tokens unwrapped and 0 wrapped —
// compiling, passing, and silently wrong. A lease is smaller and has fewer
// wrappers today, which is an argument about how long it takes to notice, not
// about whether it can happen.
//
// So the guarantee here is the type system's: a Lease that does not renew does
// not compile. This test demonstrates the consequence — a wrapper is complete
// because it could not have been written incomplete.
func TestAWrappedLeaseCannotSilentlyStopRenewing(t *testing.T) {
	inner := &beatingLease{ttl: 30 * time.Millisecond}
	var l budget.Lease = forwardingLease{inner: inner}

	ctx, stop := budget.Keepalive(context.Background(), l)
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for inner.beats.Load() < 3 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			t.Fatalf("the call was cancelled while its slot was healthy: %v", context.Cause(ctx))
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := inner.beats.Load(); got < 3 {
		t.Fatalf("a wrapped lease renewed %d times over three TTLs: the wrapper forwarded what the interface asked for, and the interface did not ask for liveness", got)
	}
}
