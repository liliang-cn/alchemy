package budget_test

import (
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// fakeClock is the injected clock. Every delay this package promises is
// asserted by reading the timer a waiter registered and by moving the clock
// past it — never by sleeping, which would test the scheduler rather than the
// backoff.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending map[*fakeTimer]struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		// A non-zero epoch, so a bug that leaves a deadline at the zero time is
		// distinguishable from one that never set it.
		now:     time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		pending: map[*fakeTimer]struct{}{},
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) budget.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t
	}
	c.pending[t] = struct{}{}
	return t
}

// Advance moves the clock and fires everything that came due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for t := range c.pending {
		if !t.deadline.After(c.now) {
			t.ch <- c.now
			delete(c.pending, t)
		}
	}
}

// remaining is how much longer each pending timer has to run, sorted, so a test
// can assert the exact delay a waiter is serving.
func (c *fakeClock) remaining() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, 0, len(c.pending))
	for t := range c.pending {
		out = append(out, t.deadline.Sub(c.now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// awaitTimers blocks until exactly n timers are pending. Reaching this point is
// proof that n callers are parked on the backoff deadline rather than merely
// slow to be scheduled; the wall-clock bound is only a guard against a hang.
func (c *fakeClock) awaitTimers(t *testing.T, n int) []time.Duration {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := c.remaining(); len(got) == n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d pending timers, have %v", n, c.remaining())
		}
		runtime.Gosched()
	}
}

type fakeTimer struct {
	c        *fakeClock
	deadline time.Time
	ch       chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	delete(t.c.pending, t)
}
