package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func TestClaimTakesTheOldestQueuedJob(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
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
	// The job a caller polls reports the same instant, so "when does this stop
	// being true" has one answer and not two.
	got, _ := s.Get(ctx, first.ID)
	if !got.ExpiresAt.Equal(l.Deadline) {
		t.Errorf("job ExpiresAt = %v, lease deadline = %v", got.ExpiresAt, l.Deadline)
	}
}

// An empty queue is not an error. A worker loop that has to distinguish "no
// work" from "the store is broken" by string matching will get it wrong.
func TestClaimOnAnEmptyQueueIsNotAnError(t *testing.T) {
	s, _ := testStore(t, Config{})
	_, ok, err := s.Claim(context.Background(), "node-a", time.Minute)
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v, want false and no error", ok, err)
	}
}

func TestALiveLeaseIsNotHandedToASecondNode(t *testing.T) {
	s, clock := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")

	if _, ok, _ := s.Claim(ctx, "node-a", time.Minute); !ok {
		t.Fatal("node-a should have got the job")
	}
	clock.Advance(59 * time.Second)
	if _, ok, _ := s.Claim(ctx, "node-b", time.Minute); ok {
		t.Fatal("node-b took a job whose lease was still alive")
	}
}

func TestHeartbeatExtendsTheLeaseAndReportsTheStage(t *testing.T) {
	s, clock := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")
	l, _, _ := s.Claim(ctx, "node-a", time.Minute)

	clock.Advance(30 * time.Second)
	l2, err := s.Heartbeat(ctx, l, "extract")
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if want := epoch.Add(90 * time.Second); !l2.Deadline.Equal(want) {
		t.Errorf("renewed deadline = %v, want %v", l2.Deadline, want)
	}
	got, _ := s.Get(ctx, "only")
	if got.Stage != "extract" {
		t.Errorf("stage = %q, want extract", got.Stage)
	}

	// A heartbeat that says nothing about the stage must not erase what the
	// worker last reported: a heartbeat loop and a progress report are usually
	// two different pieces of code.
	l3, err := s.Heartbeat(ctx, l2, "")
	if err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	if got, _ := s.Get(ctx, "only"); got.Stage != "extract" {
		t.Errorf("stage = %q after an empty heartbeat, want extract", got.Stage)
	}
	_ = l3
}

// A Lease has an unexported field on purpose: it is the store's evidence that
// Claim happened, and a struct literal is the accident it prevents.
func TestAFabricatedLeaseIsRefused(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
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
}

func TestWorkerFinishesAndReleases(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
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
	if got, _ := s.Get(ctx, lb.Job.ID); got.State != alchemy.JobPending {
		t.Errorf("state = %q, want PENDING again", got.State)
	}
	// Released work is somebody else's to take immediately.
	if l, ok, _ := s.Claim(ctx, "node-b", time.Minute); !ok || l.Job.ID != lb.Job.ID {
		t.Error("released work was not offered to another node")
	}
}

// A FAILED job with an empty Error is a job nobody can debug, so the general
// transition refuses to produce one and points at the call that carries a
// cause.
func TestFailingNeedsACauseAndTransitionSaysSo(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")
	l, _, _ := s.Claim(ctx, "node-a", time.Minute)

	err := s.Transition(ctx, l, alchemy.JobFailed)
	if err == nil {
		t.Fatal("want a refusal")
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("err = %v, want it to unwrap to ErrIllegalTransition", err)
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
}

func TestAnExpiredLeaseIsClaimableByAnotherNode(t *testing.T) {
	s, clock := testStore(t, Config{})
	ctx := context.Background()
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
}

// §8.3, the requirement in one test: two nodes briefly work the same job, and
// the second writer loses harmlessly rather than corrupting anything. The
// overtaken node here is not malicious or buggy — it was slow, finished its
// work honestly, and must simply find that the answer is already in.
func TestAnOvertakenNodeCannotOverwriteTheNodeThatTookOver(t *testing.T) {
	s, clock := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")

	slow, _, _ := s.Claim(ctx, "node-a", time.Minute)
	clock.Advance(2 * time.Minute)
	fast, _, _ := s.Claim(ctx, "node-b", time.Minute)

	if err := s.Transition(ctx, fast, alchemy.JobSucceeded); err != nil {
		t.Fatalf("node-b succeed: %v", err)
	}
	// node-a now finishes the work it started, under a lease that died.
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
	// The renewal path must lose too, or the overtaken node quietly extends a
	// lease it does not hold and writes later.
	if _, err := s.Heartbeat(ctx, slow, "extract"); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("heartbeat under a retired lease: %v", err)
	}
}

// The node name alone cannot decide this: it is the same node twice. Only a
// token that moves on every claim retires the first attempt, which is why the
// lease carries one.
func TestANodeThatReclaimsItsOwnJobRetiresItsOldLease(t *testing.T) {
	s, clock := testStore(t, Config{})
	ctx := context.Background()
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
}

// A job cancelled by its caller stops being writable immediately. The worker
// learns through the same refusal as a lost lease, so there is one path to get
// right rather than two.
func TestAWorkerWritingToACancelledJobIsRefused(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")
	l, _, _ := s.Claim(ctx, "node-a", time.Minute)

	if err := s.Cancel(ctx, "only"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := s.Transition(ctx, l, alchemy.JobSucceeded); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
	if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobCancelled {
		t.Errorf("state = %q, want CANCELLED", got.State)
	}
}

// Found by the concurrency test: a job ID outlives the job. A client retries
// under the same ID after the first job was collected and dropped, and the
// node that was working the first one comes back. If the fence restarted at 1
// for every record, that node's write from an hour ago lands on work that has
// nothing to do with it — the exact corruption the fence exists to prevent,
// arriving by the one route a per-job counter cannot see.
func TestALeaseFromAReapedJobCannotWriteToItsReplacement(t *testing.T) {
	s, clock := testStore(t, Config{DoneTTL: time.Minute, PendingTTL: time.Hour})
	ctx := context.Background()
	s.Create(ctx, "nightly")
	zombie, _, _ := s.Claim(ctx, "node-a", time.Minute)

	// node-a is partitioned. The job is withdrawn, kept for collection, and
	// eventually dropped.
	s.Cancel(ctx, "nightly")
	clock.Advance(2 * time.Minute)
	if swept, _ := s.Expire(ctx); !has(swept.Reaped, "nightly") {
		t.Fatal("the finished job should have been dropped by now")
	}

	// The client retries; node-a rejoins and picks the new job up.
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
}
