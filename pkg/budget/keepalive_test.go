package budget_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// liveLease is a lease that expires: the shape a shared store has, faked here
// so the keepalive can be tested without one. beats counts renewals, and fail
// is what the next Heartbeat returns.
type liveLease struct {
	ttl      time.Duration
	beats    atomic.Int64
	released atomic.Int64
	fail     atomic.Value // error, nil means the renewal succeeds
}

func (l *liveLease) Release(error) { l.released.Add(1) }

func (l *liveLease) TTL() time.Duration { return l.ttl }

func (l *liveLease) Heartbeat(ctx context.Context) error {
	l.beats.Add(1)
	if err, ok := l.fail.Load().(error); ok && err != nil {
		return err
	}
	return nil
}

// awaitBeats waits for at least n renewals. It is a real-time wait because a
// heartbeat interval is a real-time promise: the TTLs here are tens of
// milliseconds so the bound is a liveness guard, not the measurement.
func awaitBeats(t *testing.T, l *liveLease, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for l.beats.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d heartbeats, have %d", n, l.beats.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// A lease with no liveness must cost nothing: the local slot dies with the
// process that holds it, so there is no question a heartbeat could answer.
func TestKeepaliveIsFreeForALeaseThatCannotOutliveItsHolder(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	l, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release(nil)

	ctx := context.Background()
	got, stop := budget.Keepalive(ctx, l)
	defer stop()
	if got != ctx {
		t.Fatal("Keepalive derived a context for a lease with no TTL: the local path must allocate nothing")
	}
}

func TestKeepaliveRenewsALiveLeaseAndStopsWhenTold(t *testing.T) {
	l := &liveLease{ttl: 30 * time.Millisecond}
	ctx, stop := budget.Keepalive(context.Background(), l)

	awaitBeats(t, l, 2)
	if ctx.Err() != nil {
		t.Fatalf("the work was cancelled while the lease was healthy: %v", ctx.Err())
	}
	stop()

	// Nothing beats after the call has finished: the slot is about to be
	// returned, and a renewal after Release would extend a lease nobody holds.
	settled := l.beats.Load()
	time.Sleep(100 * time.Millisecond)
	if got := l.beats.Load(); got != settled {
		t.Fatalf("%d heartbeats arrived after stop, want none", got-settled)
	}
}

// The point of the TTL is that somebody else may take the slot. A node whose
// lease was taken must stop calling the endpoint, because the cluster has
// already given its slot away and carrying on is the overshoot the budget
// exists to prevent.
func TestALostLeaseCancelsTheWorkItWasGuarding(t *testing.T) {
	l := &liveLease{ttl: 20 * time.Millisecond}
	l.fail.Store(budget.ErrLeaseExpired)
	ctx, stop := budget.Keepalive(context.Background(), l)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the lease expired and the guarded work was never cancelled")
	}
	if !errors.Is(context.Cause(ctx), budget.ErrLeaseExpired) {
		t.Fatalf("cancel cause = %v, want ErrLeaseExpired so the caller can tell it from its own deadline", context.Cause(ctx))
	}
}

// A store that is briefly unreachable is not a lease that was taken. Cancelling
// a ninety-second call because one renewal timed out would be the failure mode
// a TTL is supposed to fix, reintroduced.
func TestATransientHeartbeatFailureDoesNotCancelTheWork(t *testing.T) {
	l := &liveLease{ttl: 20 * time.Millisecond}
	l.fail.Store(errors.New("connection reset by peer"))
	ctx, stop := budget.Keepalive(context.Background(), l)
	defer stop()

	awaitBeats(t, l, 3)
	if ctx.Err() != nil {
		t.Fatalf("a transient renewal failure cancelled the work: %v", context.Cause(ctx))
	}
}
