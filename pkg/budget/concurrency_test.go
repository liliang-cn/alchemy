package budget_test

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// peak measures how many holders were ever inside the guarded section at the
// same instant. It is the real overlap, not a count of calls: enter/leave move
// a live counter and every entry pushes the high-water mark up, so a bound that
// is broken for a single scheduling window is still caught.
type peak struct {
	live atomic.Int64
	max  atomic.Int64
}

func (p *peak) enter() {
	n := p.live.Add(1)
	for {
		old := p.max.Load()
		if n <= old || p.max.CompareAndSwap(old, n) {
			return
		}
	}
}

func (p *peak) leave() { p.live.Add(-1) }

func TestBoundHoldsUnderLoad(t *testing.T) {
	const (
		limit   = 4
		workers = 64
		rounds  = 25
	)
	b := newBudget(t, budget.Config{Limit: limit})
	var p peak
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				l, err := b.Acquire(context.Background(), "gpt")
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				p.enter()
				// Yield inside the section so goroutines genuinely overlap;
				// without it a slot can be taken and returned before any other
				// worker is scheduled and the test proves nothing.
				runtime.Gosched()
				p.leave()
				l.Release(nil)
			}
		}()
	}
	wg.Wait()

	if got := p.max.Load(); got > limit {
		t.Fatalf("peak in flight %d exceeds the budget of %d", got, limit)
	} else if got < limit {
		t.Fatalf("peak in flight was only %d with %d workers: the test never exercised the bound", got, limit)
	}
	if got := b.InFlight("gpt"); got != 0 {
		t.Fatalf("after every Release, %d slots are still held", got)
	}
}

func TestTwoModelsDoNotCompete(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1, PerModel: map[string]int{"embed": 2}})
	ctx := context.Background()

	held, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire gpt: %v", err)
	}
	defer held.Release(nil)

	// The extraction model is saturated; the embedder must be unaffected, and
	// must honour its own larger limit.
	for i := 0; i < 2; i++ {
		l, err := b.Acquire(ctx, "embed")
		if err != nil {
			t.Fatalf("Acquire embed #%d: %v", i, err)
		}
		defer l.Release(nil)
	}
	if got := b.InFlight("embed"); got != 2 {
		t.Fatalf("embed in flight = %d, want 2", got)
	}
	if got := b.InFlight("gpt"); got != 1 {
		t.Fatalf("gpt in flight = %d, want 1", got)
	}
}

func TestWaitersAreServedInArrivalOrder(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	ctx := context.Background()

	held, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	const waiters = 8
	order := make(chan int, waiters)
	leases := make(chan budget.Lease, waiters)
	for i := 0; i < waiters; i++ {
		go func(i int) {
			l, err := b.Acquire(ctx, "gpt")
			if err != nil {
				t.Errorf("waiter %d: %v", i, err)
				return
			}
			order <- i
			leases <- l
		}(i)
		// Each waiter is parked before the next one starts, so arrival order is
		// known rather than assumed.
		awaitWaiters(t, b, "gpt", i+1)
	}

	held.Release(nil)
	for want := 0; want < waiters; want++ {
		got := <-order
		if got != want {
			t.Fatalf("waiter %d was served in position %d: the queue is not FIFO", got, want)
		}
		(<-leases).Release(nil)
	}
}

func TestALongWaiterIsNotOvertakenByAStreamOfNewcomers(t *testing.T) {
	// Starvation is the failure a FIFO queue is bought to prevent: a chunk that
	// sits behind an endless stream of arrivals holds up the whole job, because
	// a job is finished only when its last chunk is.
	b := newBudget(t, budget.Config{Limit: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	held, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	early := make(chan struct{})
	go func() {
		l, err := b.Acquire(ctx, "gpt")
		if err != nil {
			t.Errorf("the early waiter never got a slot: %v", err)
			return
		}
		close(early)
		l.Release(nil)
	}()
	awaitWaiters(t, b, "gpt", 1)

	// A stream of newcomers, arriving for as long as the test runs.
	var churn sync.WaitGroup
	for i := 0; i < 16; i++ {
		churn.Add(1)
		go func() {
			defer churn.Done()
			for {
				l, err := b.Acquire(ctx, "gpt")
				if err != nil {
					return // the test is over
				}
				runtime.Gosched()
				l.Release(nil)
			}
		}()
	}
	awaitWaiters(t, b, "gpt", 17)

	held.Release(nil)
	<-early
	cancel()
	churn.Wait()
}
