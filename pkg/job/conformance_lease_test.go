package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// hold puts a job into NEEDS_REVIEW the only way a job gets there: a worker
// claims it and reports what it found.
func hold(t *testing.T, s Store, id string, why HoldReason) alchemy.Job {
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

func conformanceLease(t *testing.T, newStore factory) {
	ctx := context.Background()

	t.Run("ClaimTakesTheOldestQueuedJob", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		first, _ := s.Create(ctx, "first")
		s.Create(ctx, "second")

		l, ok, err := s.Claim(ctx, "node-a", time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if l.Job.ID != first.ID {
			t.Errorf("claimed %q, want the oldest %q", l.Job.ID, first.ID)
		}
		if l.Job.State != alchemy.JobRunning {
			t.Errorf("state = %q, want RUNNING", l.Job.State)
		}
		if l.Node != "node-a" {
			t.Errorf("lease node = %q, want node-a", l.Node)
		}
		if want := epoch.Add(time.Minute); !l.Deadline.Equal(want) {
			t.Errorf("lease deadline = %v, want %v", l.Deadline, want)
		}
		// The job a caller polls reports the same instant, so "when does this
		// stop being true" has one answer and not two.
		got, _ := s.Get(ctx, first.ID)
		if !got.ExpiresAt.Equal(l.Deadline) {
			t.Errorf("job ExpiresAt = %v, lease deadline = %v", got.ExpiresAt, l.Deadline)
		}
	})

	// An empty queue is not an error. A worker loop that has to distinguish
	// "no work" from "the store is broken" by string matching will get it wrong.
	t.Run("ClaimOnAnEmptyQueueIsNotAnError", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		if _, ok, err := s.Claim(ctx, "node-a", time.Minute); ok || err != nil {
			t.Fatalf("ok=%v err=%v, want false and no error", ok, err)
		}
	})

	t.Run("ALiveLeaseIsNotHandedToASecondNode", func(t *testing.T) {
		s, clock := newStore(t, Config{})
		s.Create(ctx, "only")
		if _, ok, _ := s.Claim(ctx, "node-a", time.Minute); !ok {
			t.Fatal("node-a should have got the job")
		}
		clock.Advance(59 * time.Second)
		if _, ok, _ := s.Claim(ctx, "node-b", time.Minute); ok {
			t.Fatal("node-b took a job whose lease was still alive")
		}
	})

	t.Run("HeartbeatExtendsTheLeaseAndReportsTheStage", func(t *testing.T) {
		s, clock := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)

		clock.Advance(30 * time.Second)
		l2, err := s.Heartbeat(ctx, l, "extract")
		if err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		// Renewed by the TTL the node asked for at Claim, not by one the store
		// picked: the node knows how long its chunks take and the store does not.
		if want := epoch.Add(90 * time.Second); !l2.Deadline.Equal(want) {
			t.Errorf("renewed deadline = %v, want %v", l2.Deadline, want)
		}
		if got, _ := s.Get(ctx, "only"); got.Stage != "extract" {
			t.Errorf("stage = %q, want extract", got.Stage)
		}
		// A heartbeat that says nothing about the stage must not erase what the
		// worker last reported: a heartbeat loop and a progress report are
		// usually two different pieces of code.
		if _, err := s.Heartbeat(ctx, l2, ""); err != nil {
			t.Fatalf("second heartbeat: %v", err)
		}
		if got, _ := s.Get(ctx, "only"); got.Stage != "extract" {
			t.Errorf("stage = %q after an empty heartbeat, want extract", got.Stage)
		}
	})

	// A Lease has an unexported field on purpose: it is the store's evidence
	// that Claim happened, and a struct literal is the accident it prevents.
	t.Run("AFabricatedLeaseIsRefused", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		j, _ := s.Create(ctx, "only")
		s.Claim(ctx, "node-a", time.Minute)

		forged := Lease{Job: j, Node: "node-a", Deadline: epoch.Add(time.Hour)}
		if _, err := s.Heartbeat(ctx, forged, ""); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("err = %v, want ErrLeaseLost", err)
		}
		if err := s.Transition(ctx, forged, alchemy.JobSucceeded); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("err = %v, want ErrLeaseLost", err)
		}
		if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobRunning {
			t.Errorf("state = %q, a forged lease changed the job", got.State)
		}
	})

	t.Run("WorkerFinishesAndReleases", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "a")
		s.Create(ctx, "b")

		la, _, _ := s.Claim(ctx, "node-a", time.Minute)
		if err := s.Transition(ctx, la, alchemy.JobSucceeded); err != nil {
			t.Fatalf("succeed: %v", err)
		}
		if got, _ := s.Get(ctx, la.Job.ID); got.State != alchemy.JobSucceeded {
			t.Errorf("state = %q, want SUCCEEDED", got.State)
		}
		lb, _, _ := s.Claim(ctx, "node-a", time.Minute)
		if err := s.Release(ctx, lb); err != nil {
			t.Fatalf("release: %v", err)
		}
		got, _ := s.Get(ctx, lb.Job.ID)
		if got.State != alchemy.JobPending {
			t.Errorf("state = %q, want PENDING again", got.State)
		}
		// The stage goes with the node. Leaving "extract" on a job nobody is
		// working is how a progress display lies.
		if got.Stage != "" {
			t.Errorf("stage = %q on released work, want it cleared", got.Stage)
		}
		if l, ok, _ := s.Claim(ctx, "node-b", time.Minute); !ok || l.Job.ID != lb.Job.ID {
			t.Error("released work was not offered to another node")
		}
	})

	// A FAILED job with an empty Error is a job nobody can debug, so the
	// general transition refuses to produce one and points at the call that
	// carries a cause.
	t.Run("FailingNeedsACauseAndTransitionSaysSo", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)

		err := s.Transition(ctx, l, alchemy.JobFailed)
		if err == nil {
			t.Fatal("want a refusal")
		}
		if !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("err = %v, want it to unwrap to ErrIllegalTransition", err)
		}
		if err := s.Fail(ctx, l, ""); err == nil {
			t.Error("a failure with an empty cause must be refused")
		}
		if err := s.Fail(ctx, l, "model endpoint refused 47 chunks"); err != nil {
			t.Fatalf("fail: %v", err)
		}
		got, _ := s.Get(ctx, "only")
		if got.State != alchemy.JobFailed {
			t.Errorf("state = %q, want FAILED", got.State)
		}
		if got.Error == "" {
			t.Error("a failed job must carry why")
		}
	})

	t.Run("AnExpiredLeaseIsClaimableByAnotherNode", func(t *testing.T) {
		s, clock := newStore(t, Config{})
		s.Create(ctx, "only")
		if _, ok, _ := s.Claim(ctx, "node-a", time.Minute); !ok {
			t.Fatal("node-a should have got the job")
		}
		clock.Advance(time.Minute) // node-a's lease dies exactly now.
		l, ok, err := s.Claim(ctx, "node-b", time.Minute)
		if err != nil || !ok {
			t.Fatalf("node-b claim: ok=%v err=%v", ok, err)
		}
		if l.Job.ID != "only" || l.Node != "node-b" {
			t.Fatalf("node-b got %+v", l)
		}
	})

	// §8.3, the requirement in one test: two nodes briefly work the same job,
	// and the second writer loses harmlessly rather than corrupting anything.
	// The overtaken node is not malicious or buggy — it was slow, finished its
	// work honestly, and must simply find that the answer is already in.
	t.Run("AnOvertakenNodeCannotOverwriteTheNodeThatTookOver", func(t *testing.T) {
		s, clock := newStore(t, Config{})
		s.Create(ctx, "only")

		slow, _, _ := s.Claim(ctx, "node-a", time.Minute)
		clock.Advance(2 * time.Minute)
		fast, _, _ := s.Claim(ctx, "node-b", time.Minute)

		if err := s.Transition(ctx, fast, alchemy.JobSucceeded); err != nil {
			t.Fatalf("node-b succeed: %v", err)
		}
		err := s.Fail(ctx, slow, "node-a thought this failed")
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("node-a's late write: err = %v, want ErrLeaseLost", err)
		}
		var lost *LeaseError
		if !errors.As(err, &lost) || lost.State != alchemy.JobSucceeded {
			t.Errorf("err = %v, want it to name the state that won", err)
		}
		if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobSucceeded || got.Error != "" {
			t.Errorf("job = %+v, want node-b's SUCCEEDED untouched", got)
		}
		if _, err := s.Heartbeat(ctx, slow, "extract"); !errors.Is(err, ErrLeaseLost) {
			t.Errorf("heartbeat under a retired lease: %v", err)
		}
	})

	// The node name alone cannot decide this: it is the same node twice. Only a
	// token that moves on every claim retires the first attempt.
	t.Run("ANodeThatReclaimsItsOwnJobRetiresItsOldLease", func(t *testing.T) {
		s, clock := newStore(t, Config{})
		s.Create(ctx, "only")

		first, _, _ := s.Claim(ctx, "node-a", time.Minute)
		clock.Advance(2 * time.Minute)
		second, _, _ := s.Claim(ctx, "node-a", time.Minute) // same node, new attempt.

		if second.token == first.token {
			t.Fatal("a re-claim must mint a new token or the old attempt still writes")
		}
		if err := s.Transition(ctx, first, alchemy.JobSucceeded); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("the abandoned attempt wrote: %v", err)
		}
		if err := s.Transition(ctx, second, alchemy.JobSucceeded); err != nil {
			t.Fatalf("the live attempt was refused: %v", err)
		}
	})

	// A job cancelled by its caller stops being writable immediately. The
	// worker learns through the same refusal as a lost lease, so there is one
	// path to get right rather than two — and the refusal has to say which of
	// the two happened, because "cancelled" and "overtaken" are different bugs.
	t.Run("AWorkerWritingToACancelledJobIsRefused", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)

		if err := s.Cancel(ctx, "only"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		err := s.Transition(ctx, l, alchemy.JobSucceeded)
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("err = %v, want ErrLeaseLost", err)
		}
		var lost *LeaseError
		if !errors.As(err, &lost) || lost.State != alchemy.JobCancelled {
			t.Errorf("err = %v, want the refusal to say the job was cancelled", err)
		}
		if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobCancelled {
			t.Errorf("state = %q, want CANCELLED", got.State)
		}
	})

	// Found by the in-memory store's concurrency test: a job ID outlives the
	// job. A client retries under the same ID after the first job was collected
	// and dropped, and the node that was working the first one comes back. If
	// the fence restarted at 1 for every record — or were a column on the row —
	// that node's write from an hour ago lands on work that has nothing to do
	// with it.
	t.Run("ALeaseFromAReapedJobCannotWriteToItsReplacement", func(t *testing.T) {
		s, clock := newStore(t, Config{DoneTTL: time.Minute, PendingTTL: time.Hour})
		s.Create(ctx, "nightly")
		zombie, _, _ := s.Claim(ctx, "node-a", time.Minute)

		s.Cancel(ctx, "nightly")
		clock.Advance(2 * time.Minute)
		if swept, _ := s.Expire(ctx); !has(swept.Reaped, "nightly") {
			t.Fatal("the finished job should have been dropped by now")
		}
		if _, err := s.Create(ctx, "nightly"); err != nil {
			t.Fatalf("recreate: %v", err)
		}
		fresh, ok, _ := s.Claim(ctx, "node-a", time.Minute)
		if !ok {
			t.Fatal("the replacement job was not claimable")
		}
		if fresh.token == zombie.token {
			t.Fatalf("token %d reused across two jobs sharing an ID", fresh.token)
		}
		if err := s.Transition(ctx, zombie, alchemy.JobSucceeded); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("a lease from the reaped job wrote to its replacement: %v", err)
		}
		if got, _ := s.Get(ctx, "nightly"); got.State != alchemy.JobRunning {
			t.Errorf("state = %q, want the replacement still RUNNING", got.State)
		}
	})
}

func conformanceReview(t *testing.T, newStore factory) {
	ctx := context.Background()

	// §7.3's first mechanic. Optional review work can expire cheaply; a job
	// blocked on a real question should outlive a long weekend, because someone
	// has to be found first. Two timers, not one.
	t.Run("AConflictHoldOutlivesAReviewHold", func(t *testing.T) {
		s, _ := newStore(t, Config{ReviewTTL: 2 * time.Hour, ConflictTTL: 72 * time.Hour})
		review := hold(t, s, "review", HoldReview)
		conflict := hold(t, s, "conflict", HoldConflict)

		if want := epoch.Add(2 * time.Hour); !review.ExpiresAt.Equal(want) {
			t.Errorf("review hold expires %v, want %v", review.ExpiresAt, want)
		}
		if want := epoch.Add(72 * time.Hour); !conflict.ExpiresAt.Equal(want) {
			t.Errorf("conflict hold expires %v, want %v", conflict.ExpiresAt, want)
		}
		if !conflict.ExpiresAt.After(review.ExpiresAt) {
			t.Error("a job blocked on a real question must outlive one merely offered for review")
		}
		if review.State != alchemy.JobNeedsReview || conflict.State != alchemy.JobNeedsReview {
			t.Errorf("states %q and %q, want both NEEDS_REVIEW", review.State, conflict.State)
		}
	})

	// A hold releases the lease: nobody is working the job, a person is.
	t.Run("AHeldJobIsNobodysAndIsNotHandedOut", func(t *testing.T) {
		s, clock := newStore(t, Config{})
		hold(t, s, "only", HoldConflict)
		clock.Advance(time.Hour)
		if _, ok, _ := s.Claim(ctx, "node-b", time.Minute); ok {
			t.Error("a held job was handed to a node; only a person can answer it")
		}
	})

	// A worker cannot answer the question it asked. §7.3: a conflict is a
	// question and questions have to be asked of someone.
	t.Run("AWorkerCannotResolveItsOwnHold", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)
		if err := s.Hold(ctx, l, HoldConflict); err != nil {
			t.Fatalf("hold: %v", err)
		}
		if err := s.Transition(ctx, l, alchemy.JobSucceeded); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("err = %v, want the worker's lease to be gone", err)
		}
	})

	t.Run("ResolveAcceptsAHeldJob", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		hold(t, s, "only", HoldConflict)
		if err := s.Resolve(ctx, "only", alchemy.JobSucceeded); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobSucceeded {
			t.Errorf("state = %q, want SUCCEEDED", got.State)
		}
	})

	t.Run("ResolveRefusesJobsThatAskedNoQuestion", func(t *testing.T) {
		s, _ := newStore(t, Config{})
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
		if err := s.Resolve(ctx, "no-such-job", alchemy.JobSucceeded); !errors.Is(err, ErrNotFound) {
			t.Errorf("resolve of an unknown job = %v, want ErrNotFound", err)
		}
		// "There is no such job" outranks "that move would be illegal". A
		// reviewer who mistypes an ID and is told the transition is wrong will
		// go looking for a job that was never there.
		if err := s.Resolve(ctx, "no-such-job", alchemy.JobRunning); !errors.Is(err, ErrNotFound) {
			t.Errorf("resolve of an unknown job with an illegal target = %v, want ErrNotFound", err)
		}
	})

	t.Run("HoldNeedsAKnownReason", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)
		if err := s.Hold(ctx, l, HoldReason(0)); err == nil {
			t.Fatal("a hold with no reason cannot pick a timer and must be refused")
		}
		if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobRunning {
			t.Errorf("state = %q, want the job untouched", got.State)
		}
	})

	t.Run("CancelOfAnUnknownJobIsNotFound", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		if err := s.Cancel(ctx, "no-such-job"); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	// A finished job is finished. A cancel arriving after it succeeded must be
	// refused with the table's own words, not applied.
	t.Run("CancelOfAFinishedJobIsRefusedWithAReason", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)
		s.Transition(ctx, l, alchemy.JobSucceeded)

		err := s.Cancel(ctx, "only")
		if !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("err = %v, want ErrIllegalTransition", err)
		}
		var refused *TransitionError
		if !errors.As(err, &refused) || refused.From != alchemy.JobSucceeded {
			t.Errorf("err = %v, want it to name the state the job was in", err)
		}
	})
}

// RUNNING -> RUNNING is in the transition table — it is the takeover edge, and
// state_test.go calls it the only self-transition. Claim is how it is normally
// reached, but Transition accepts it too, and a store that quietly treated it
// as "some other state" would destroy the lease of a worker that made a call
// the table says is legal.
//
// This is the kind of edge that only a table-driven check finds: nothing in
// the service would ever write it, which is exactly why neither store would
// have been tested on it.
func conformanceSelfTransition(t *testing.T, newStore factory) {
	ctx := context.Background()
	t.Run("TheTakeoverEdgeDoesNotDestroyTheLeaseThatUsesIt", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)

		if err := s.Transition(ctx, l, alchemy.JobRunning); err != nil {
			t.Fatalf("the table's own self-transition was refused: %v", err)
		}
		got, err := s.Get(ctx, "only")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.State != alchemy.JobRunning {
			t.Errorf("state = %q, want RUNNING", got.State)
		}
		if !got.ExpiresAt.Equal(l.Deadline) {
			t.Errorf("ExpiresAt = %v, want the lease deadline %v: the job's expiry "+
				"stopped being its lease", got.ExpiresAt, l.Deadline)
		}
		// And the lease still works, which is the whole point.
		if _, err := s.Heartbeat(ctx, l, "extract"); err != nil {
			t.Errorf("heartbeat after the self-transition: %v", err)
		}
		if err := s.Transition(ctx, l, alchemy.JobSucceeded); err != nil {
			t.Errorf("finish after the self-transition: %v", err)
		}
	})
}
