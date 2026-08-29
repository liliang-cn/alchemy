package budget_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// tooMany is what an adapter hands back on a 429.
func tooMany(after time.Duration) error { return budget.TooFast(errors.New("HTTP 429"), after) }

// errProbe ends a call that was only made to observe how long the endpoint
// held it back. It is deliberately neither nil nor a rate limit: a probe that
// reported success would reset the schedule it is trying to measure, which is
// the correct behaviour for a real call and useless for this one.
var errProbe = errors.New("probe")

// backoffBudget is a budget whose clock and jitter are both injected, so every
// delay below is an exact number.
func backoffBudget(t *testing.T, limit int, p budget.Backoff) (*budget.Local, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	b := newBudget(t, budget.Config{
		Limit:   limit,
		Backoff: p,
		Clock:   clk,
		// Draw 1.0 means "the full nominal delay", so the schedule is readable.
		Rand: fixedRand(1),
	})
	return b, clk
}

func TestARateLimitFromOneWorkerHoldsBackTheOthers(t *testing.T) {
	b, clk := backoffBudget(t, 3, budget.Backoff{Base: 4 * time.Second, Max: time.Minute})
	ctx := context.Background()

	// One worker calls, is rate limited, and reports it. It is the endpoint
	// that is now in backoff, not this caller.
	first, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	first.Release(tooMany(0))

	const others = 2
	in := make(chan budget.Lease, others)
	for i := 0; i < others; i++ {
		go func() {
			l, err := b.Acquire(ctx, "gpt")
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			in <- l
		}()
	}

	// Both other workers are parked on the endpoint's deadline even though two
	// of the three slots are free — that is the difference between a budget and
	// a semaphore.
	if got := clk.awaitTimers(t, others); got[0] != 4*time.Second || got[1] != 4*time.Second {
		t.Fatalf("waiting workers are serving %v, want two waits of 4s", got)
	}
	select {
	case <-in:
		t.Fatal("a worker got through while the endpoint was in backoff")
	default:
	}

	// Part-way through, nobody moves.
	clk.Advance(3 * time.Second)
	if got := clk.awaitTimers(t, others); got[0] != time.Second {
		t.Fatalf("after 3s of a 4s backoff the remaining wait is %v, want 1s", got[0])
	}
	select {
	case <-in:
		t.Fatal("a worker got through before the backoff expired")
	default:
	}

	clk.Advance(time.Second)
	for i := 0; i < others; i++ {
		(<-in).Release(nil)
	}
}

func TestManyRateLimitsInOneWindowAreOneRound(t *testing.T) {
	b, clk := backoffBudget(t, 8, budget.Backoff{Base: 4 * time.Second, Max: time.Minute})
	ctx := context.Background()

	// Eight in-flight calls all come back 429 at once: the usual shape of a
	// rate limit, since they were all sent before the first refusal arrived.
	// Escalating once per report would take the delay to 2^8 × base for what is
	// a single refusal by the endpoint.
	var held []budget.Lease
	for i := 0; i < 8; i++ {
		l, err := b.Acquire(ctx, "gpt")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		held = append(held, l)
	}
	for _, l := range held {
		l.Release(tooMany(0))
	}

	go func() {
		l, err := b.Acquire(ctx, "gpt")
		if err == nil {
			l.Release(nil)
		}
	}()
	if got := clk.awaitTimers(t, 1); got[0] != 4*time.Second {
		t.Fatalf("after eight 429s in one window the wait is %v, want the first round's 4s", got[0])
	}
	clk.Advance(4 * time.Second)
}

func TestASecondRoundBacksOffFurtherAndSuccessResetsIt(t *testing.T) {
	b, clk := backoffBudget(t, 1, budget.Backoff{Base: 4 * time.Second, Max: time.Minute})
	ctx := context.Background()

	round := func(outcome error) time.Duration {
		t.Helper()
		l, err := b.Acquire(ctx, "gpt")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		l.Release(outcome)
		if outcome == nil {
			return 0
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			l, err := b.Acquire(ctx, "gpt")
			if err == nil {
				l.Release(errProbe)
			}
		}()
		d := clk.awaitTimers(t, 1)[0]
		clk.Advance(d)
		<-done
		return d
	}

	if got := round(tooMany(0)); got != 4*time.Second {
		t.Fatalf("first round waited %v, want 4s", got)
	}
	if got := round(tooMany(0)); got != 8*time.Second {
		t.Fatalf("second round waited %v, want 8s", got)
	}
	if got := round(tooMany(0)); got != 16*time.Second {
		t.Fatalf("third round waited %v, want 16s", got)
	}

	// A call that succeeds is the endpoint saying it is serving again, so the
	// next refusal starts from the bottom rather than from where we left off an
	// hour ago.
	round(nil)
	if got := round(tooMany(0)); got != 4*time.Second {
		t.Fatalf("after a success the next round waited %v, want the first round's 4s", got)
	}
}

func TestTheEndpointsOwnRetryAfterWinsButIsStillBounded(t *testing.T) {
	b, clk := backoffBudget(t, 1, budget.Backoff{Base: 4 * time.Second, Max: 30 * time.Second})
	ctx := context.Background()

	wait := func(outcome error) time.Duration {
		t.Helper()
		l, err := b.Acquire(ctx, "gpt")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		l.Release(outcome)
		done := make(chan struct{})
		go func() {
			defer close(done)
			l, err := b.Acquire(ctx, "gpt")
			if err == nil {
				l.Release(errProbe)
			}
		}()
		d := clk.awaitTimers(t, 1)[0]
		clk.Advance(d)
		<-done
		return d
	}

	// The endpoint knows its own window; when it asks for longer than our
	// guess, its number is the better information.
	if got := wait(tooMany(20 * time.Second)); got != 20*time.Second {
		t.Fatalf("waited %v, want the endpoint's 20s", got)
	}
	// A Retry-After shorter than our schedule does not shorten it: we are
	// already the ones who were too fast.
	if got := wait(tooMany(time.Second)); got != 8*time.Second {
		t.Fatalf("waited %v, want our own 8s", got)
	}
	// And an implausible one is still bounded by Max, or one bad header stalls
	// a job for a day.
	if got := wait(tooMany(24 * time.Hour)); got != 30*time.Second {
		t.Fatalf("waited %v, want the cap of 30s", got)
	}
}

func TestAnOrdinaryErrorDoesNotCloseTheEndpoint(t *testing.T) {
	b, clk := backoffBudget(t, 1, budget.Backoff{Base: 4 * time.Second, Max: time.Minute})
	ctx := context.Background()

	l, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release(errors.New("connection reset"))

	next, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire after an ordinary error: %v", err)
	}
	next.Release(nil)
	if got := clk.remaining(); len(got) != 0 {
		t.Fatalf("a non-rate-limit error started a backoff of %v", got)
	}
}

func TestCancelWhileInBackoffReturnsTheSlot(t *testing.T) {
	b, clk := backoffBudget(t, 1, budget.Backoff{Base: 10 * time.Second, Max: time.Minute})

	l, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release(tooMany(0))

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		l, err := b.Acquire(ctx, "gpt")
		if err == nil {
			l.Release(nil)
		}
		got <- err
	}()
	clk.awaitTimers(t, 1)

	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire = %v, want context.Canceled", err)
	}
	if got := b.InFlight("gpt"); got != 0 {
		t.Fatalf("in flight = %d after a wait abandoned during backoff, want 0", got)
	}
	if got := clk.remaining(); len(got) != 0 {
		t.Fatalf("%v timers left running after the wait was abandoned", got)
	}

	// The endpoint is still in backoff for everyone else; the slot is free.
	done := make(chan struct{})
	go func() {
		defer close(done)
		l, err := b.Acquire(context.Background(), "gpt")
		if err == nil {
			l.Release(nil)
		}
	}()
	clk.awaitTimers(t, 1)
	clk.Advance(10 * time.Second)
	<-done
}
