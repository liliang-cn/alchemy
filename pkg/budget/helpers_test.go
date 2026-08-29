package budget_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// newBudget builds a Local or fails the test, so no test body carries the
// constructor's error handling twice.
func newBudget(t *testing.T, cfg budget.Config) *budget.Local {
	t.Helper()
	b, err := budget.NewLocal(cfg)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return b
}

// awaitWaiters blocks until n goroutines are parked in Acquire for model. It
// spins rather than sleeps: the wall-clock deadline is a liveness guard that
// turns a hang into a readable failure, never the thing being measured. Every
// timing assertion in this package is made against the injected clock instead.
func awaitWaiters(t *testing.T, b *budget.Local, model string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := b.Waiting(model); got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d waiters on %q, have %d", n, model, b.Waiting(model))
		}
		runtime.Gosched()
	}
}
