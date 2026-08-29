package budget_test

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

func TestCancelWhileWaitingReturnsPromptlyAndLeaksNoSlot(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})

	held, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		l, err := b.Acquire(ctx, "gpt")
		if err == nil {
			l.Release(nil)
		}
		got <- err
	}()
	awaitWaiters(t, b, "gpt", 1)

	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire after cancel = %v, want context.Canceled", err)
	}
	if got := b.Waiting("gpt"); got != 0 {
		t.Fatalf("%d waiters still queued after the only one gave up", got)
	}

	// The abandoned wait must not have consumed the slot it never got: after
	// the holder releases, the budget is whole again.
	held.Release(nil)
	for i := 0; i < 3; i++ {
		l, err := b.Acquire(context.Background(), "gpt")
		if err != nil {
			t.Fatalf("Acquire #%d after a cancelled wait: %v", i, err)
		}
		l.Release(nil)
	}
	if got := b.InFlight("gpt"); got != 0 {
		t.Fatalf("in flight = %d, want 0", got)
	}
}

func TestAcquireWithADeadContextTakesNoSlot(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := b.Acquire(ctx, "gpt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire = %v, want context.Canceled", err)
	}
	if got := b.InFlight("gpt"); got != 0 {
		t.Fatalf("in flight = %d after a refused Acquire, want 0", got)
	}
}

func TestReleasingTwiceDoesNotWidenTheBudget(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	l, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release(nil)
	l.Release(nil)

	if got := b.InFlight("gpt"); got != 0 {
		t.Fatalf("in flight = %d after a double Release, want 0", got)
	}
	first, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer first.Release(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan error, 1)
	go func() {
		l, err := b.Acquire(ctx, "gpt")
		if err == nil {
			l.Release(nil)
		}
		got <- err
	}()
	awaitWaiters(t, b, "gpt", 1)
	select {
	case err := <-got:
		t.Fatalf("a second caller got in (err=%v): the double Release created a phantom slot", err)
	default:
	}
}
