package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func held(t *testing.T, s *Mem, id string, why HoldReason) alchemy.Job {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Create(ctx, id); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	l, ok, err := s.Claim(ctx, "node-a", time.Minute)
	if !ok || err != nil {
		t.Fatalf("claim %s: ok=%v err=%v", id, ok, err)
	}
	if err := s.Hold(ctx, l, why); err != nil {
		t.Fatalf("hold %s: %v", id, err)
	}
	j, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return j
}

// §7.3's first mechanic. Optional review work can expire cheaply; a job
// blocked on a real question should outlive a long weekend, because someone
// has to be found first. Two timers, not one.
func TestAConflictHoldOutlivesAReviewHold(t *testing.T) {
	s, _ := testStore(t, Config{ReviewTTL: 2 * time.Hour, ConflictTTL: 72 * time.Hour})

	review := held(t, s, "review", HoldReview)
	conflict := held(t, s, "conflict", HoldConflict)

	if want := epoch.Add(2 * time.Hour); !review.ExpiresAt.Equal(want) {
		t.Errorf("review hold expires %v, want %v", review.ExpiresAt, want)
	}
	if want := epoch.Add(72 * time.Hour); !conflict.ExpiresAt.Equal(want) {
		t.Errorf("conflict hold expires %v, want %v", conflict.ExpiresAt, want)
	}
	if !conflict.ExpiresAt.After(review.ExpiresAt) {
		t.Error("a job blocked on a real question must outlive one merely offered for review")
	}
	// §7.3: NEEDS_REVIEW is reached without review mode being on, so the state
	// is the same for both and only the timer differs.
	if review.State != alchemy.JobNeedsReview || conflict.State != alchemy.JobNeedsReview {
		t.Errorf("states %q and %q, want both NEEDS_REVIEW", review.State, conflict.State)
	}
}

// The defaults must not accidentally make the two the same length, since a
// store built from a zero Config is what a buyer evaluating the product runs.
func TestTheDefaultsKeepTheTwoTimersApart(t *testing.T) {
	s, _ := testStore(t, Config{})
	if s.cfg.ConflictTTL <= s.cfg.ReviewTTL {
		t.Errorf("default ConflictTTL %v must exceed ReviewTTL %v", s.cfg.ConflictTTL, s.cfg.ReviewTTL)
	}
}

// A hold releases the lease: nobody is working the job, a person is. Leaving
// the lease alive would keep a node's heartbeat responsible for work that is
// waiting on a human, and lose the job when that node restarts.
func TestAHeldJobIsNobodysAndIsNotHandedOut(t *testing.T) {
	s, clock := testStore(t, Config{})
	ctx := context.Background()
	held(t, s, "only", HoldConflict)

	clock.Advance(time.Hour)
	if _, ok, _ := s.Claim(ctx, "node-b", time.Minute); ok {
		t.Error("a held job was handed to a node; only a person can answer it")
	}
}

// A worker cannot answer the question it asked. §7.3: a conflict is a question
// and questions have to be asked of someone.
func TestAWorkerCannotResolveItsOwnHold(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")
	l, _, _ := s.Claim(ctx, "node-a", time.Minute)
	if err := s.Hold(ctx, l, HoldConflict); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := s.Transition(ctx, l, alchemy.JobSucceeded); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want the worker's lease to be gone", err)
	}
}

func TestResolveAcceptsAHeldJob(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
	held(t, s, "only", HoldConflict)

	if err := s.Resolve(ctx, "only", alchemy.JobSucceeded); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, _ := s.Get(ctx, "only")
	if got.State != alchemy.JobSucceeded {
		t.Errorf("state = %q, want SUCCEEDED", got.State)
	}
}

func TestResolveRefusesJobsThatAskedNoQuestion(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "queued")

	err := s.Resolve(ctx, "queued", alchemy.JobSucceeded)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	if got, _ := s.Get(ctx, "queued"); got.State != alchemy.JobPending {
		t.Errorf("state = %q, the refusal still changed the job", got.State)
	}
	if err := s.Resolve(ctx, "queued", alchemy.JobRunning); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("a caller started a job without a lease: %v", err)
	}
}

func TestHoldNeedsAKnownReason(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")
	l, _, _ := s.Claim(ctx, "node-a", time.Minute)
	if err := s.Hold(ctx, l, HoldReason(0)); err == nil {
		t.Fatal("a hold with no reason cannot pick a timer and must be refused")
	}
	if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobRunning {
		t.Errorf("state = %q, want the job untouched", got.State)
	}
}
