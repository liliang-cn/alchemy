package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// spyBudget is a budget that grants everything and remembers who asked. The
// question this test asks is not whether pkg/budget bounds concurrency — it
// has its own tests for that — but whether the pipeline goes through it at
// all, which is the only part that can be got wrong from here.
type spyBudget struct {
	mu       sync.Mutex
	acquired map[string]int
}

func (s *spyBudget) Acquire(_ context.Context, model string) (budget.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acquired == nil {
		s.acquired = map[string]int{}
	}
	s.acquired[model]++
	return noopLease{}, nil
}

func (s *spyBudget) count(model string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquired[model]
}

type noopLease struct{}

func (noopLease) Release(error) {}

// §8.2 makes model concurrency a property of the endpoint rather than of a
// worker, and pkg/budget's own doc comment says how that reaches a stage:
// "Nothing in the pipeline imports this package. A stage is budgeted by being
// handed a wrapped model." So the pipeline budgets by wrapping, and no stage
// learns that a budget exists.
func TestABudgetIsAppliedByWrappingTheModels(t *testing.T) {
	spy := &spyBudget{}
	req := mixedJob(t)
	req.Budget = spy
	if _, err := Run(context.Background(), req, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Keyed by the model's own name, which is what pkg/budget documents as the
	// endpoint key, so the budget and the provenance agree on what an endpoint
	// is.
	if got := spy.count("gemini-3.6-flash-high"); got != 2 {
		t.Errorf("the extraction took %d slots, want one per chunk (2)", got)
	}
	if got := spy.count("fake-embed-3"); got == 0 {
		t.Error("the embedder was not budgeted")
	}
}

// The budget is optional, and a job without one is not a job running against
// an unbounded budget — it is a job whose caller has not declared an endpoint
// limit, which is the single-node default (§8.3).
func TestAJobWithNoBudgetRuns(t *testing.T) {
	if _, err := Run(context.Background(), mixedJob(t), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
