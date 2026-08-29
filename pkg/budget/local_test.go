package budget_test

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

func TestNewLocalRejectsABudgetWithNoBound(t *testing.T) {
	if _, err := budget.NewLocal(budget.Config{Limit: 0}); err == nil {
		t.Fatal("want an error for Limit 0: a budget with no bound is not a budget")
	}
	if _, err := budget.NewLocal(budget.Config{Limit: 4, PerModel: map[string]int{"gpt": -1}}); err == nil {
		t.Fatal("want an error for a negative per-model limit")
	}
	if _, err := budget.NewLocal(budget.Config{Limit: 1}); err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
}

func TestSecondAcquireWaitsUntilTheFirstIsReleased(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	ctx := context.Background()

	first, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

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
		t.Fatalf("second Acquire returned %v while the only slot was held", err)
	default:
	}

	first.Release(nil)
	if err := <-got; err != nil {
		t.Fatalf("second Acquire after Release: %v", err)
	}
}
