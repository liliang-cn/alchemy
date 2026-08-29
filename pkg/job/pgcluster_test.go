package job

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Two Store values over one database, which is the only configuration this
// implementation exists for. Everything above this file could be satisfied by
// a store that happened to keep its state in Postgres; these are the tests
// that a *cluster* passes.
//
// Both nodes share one ManualClock, and that is not a shortcut — it is the
// model. The production configuration has exactly one clock, the database's,
// and a test in which each node had its own would be testing a store nobody
// should deploy. The store that does have its own clock is tested for, in
// pgclock_test.go, as the hazard it is.

func twoNodes(t *testing.T, cfg Config) (*PG, *PG, *ManualClock) {
	t.Helper()
	f := newFixture(t)
	clock := NewManualClock(epoch)
	cfg.Clock = clock
	return f.open(t, PGConfig{Config: cfg}), f.open(t, PGConfig{Config: cfg}), clock
}

// §8.3, the sentence that requires this whole file: a node that dies mid-job
// must not take the job with it.
func TestALeaseExpiringLetsTheOtherNodeTakeTheJob(t *testing.T) {
	a, b, clock := twoNodes(t, Config{PendingTTL: time.Hour})
	ctx := context.Background()

	if _, err := a.Create(ctx, "import"); err != nil {
		t.Fatalf("create: %v", err)
	}
	la, ok, err := a.Claim(ctx, "node-a", time.Minute)
	if !ok || err != nil {
		t.Fatalf("node-a claim: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := b.Claim(ctx, "node-b", time.Minute); ok {
		t.Fatal("node-b took a job node-a holds a live lease on")
	}

	// node-a stops answering. Nobody releases anything; the lease simply dies.
	clock.Advance(2 * time.Minute)

	lb, ok, err := b.Claim(ctx, "node-b", time.Minute)
	if !ok || err != nil {
		t.Fatalf("node-b claim after the lease died: ok=%v err=%v", ok, err)
	}
	if lb.Job.ID != "import" {
		t.Fatalf("node-b got %q", lb.Job.ID)
	}
	if lb.token <= la.token {
		t.Errorf("takeover token %d does not exceed %d", lb.token, la.token)
	}
	// The takeover keeps the stage the dead node reported: the work is being
	// picked up, not started again, and §8.2's content-addressed cache means
	// the re-run costs the chunks that had not finished.
	if got, _ := b.Get(ctx, "import"); got.State != alchemy.JobRunning {
		t.Errorf("state = %q, want RUNNING under node-b", got.State)
	}
}

// The other half, and the one §8.3 says must be survivable rather than
// prevented: node-a was merely slow, comes back, and finishes work node-b has
// already completed. It must lose, and it must be told which of the ways it
// lost — "somebody else finished this" and "your caller withdrew it" are
// different things for the operator reading the log.
func TestANodeFinishingUnderARetiredLeaseLosesHarmlessly(t *testing.T) {
	a, b, clock := twoNodes(t, Config{})
	ctx := context.Background()

	a.Create(ctx, "import")
	slow, _, _ := a.Claim(ctx, "node-a", time.Minute)
	clock.Advance(2 * time.Minute)
	fast, ok, _ := b.Claim(ctx, "node-b", time.Minute)
	if !ok {
		t.Fatal("node-b did not get the abandoned job")
	}

	if err := b.Transition(ctx, fast, alchemy.JobSucceeded); err != nil {
		t.Fatalf("node-b succeed: %v", err)
	}

	// node-a finishes honestly, an hour late.
	err := a.Fail(ctx, slow, "node-a thought this failed")
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("node-a's late write: %v, want ErrLeaseLost", err)
	}
	var lost *LeaseError
	if !errors.As(err, &lost) {
		t.Fatalf("err = %v, want a *LeaseError", err)
	}
	if lost.State != alchemy.JobSucceeded {
		t.Errorf("LeaseError.State = %q, want the state that won", lost.State)
	}
	if msg := err.Error(); msg == "" {
		t.Error("a refusal with no message is a log line nobody can act on")
	}
	if got, _ := a.Get(ctx, "import"); got.State != alchemy.JobSucceeded || got.Error != "" {
		t.Errorf("job = %+v, want node-b's SUCCEEDED untouched", got)
	}
	// Renewal must lose too, or the overtaken node quietly extends a lease it
	// does not hold and writes later.
	if _, err := a.Heartbeat(ctx, slow, "extract"); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("heartbeat under a retired lease: %v", err)
	}
}

// A worker that finds its job withdrawn must be told it was cancelled, not
// merely that it lost. Zero rows updated does not carry that; the refusal has
// to go and look.
func TestTheRefusalSaysWhetherTheJobWasCancelledOrTakenOverOrReaped(t *testing.T) {
	a, b, clock := twoNodes(t, Config{DoneTTL: time.Minute})
	ctx := context.Background()

	// Cancelled underneath the worker.
	a.Create(ctx, "cancelled")
	lc, _, _ := a.Claim(ctx, "node-a", time.Minute)
	if err := b.Cancel(ctx, "cancelled"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var lost *LeaseError
	err := a.Transition(ctx, lc, alchemy.JobSucceeded)
	if !errors.As(err, &lost) || lost.State != alchemy.JobCancelled {
		t.Errorf("err = %v, want a LeaseError naming CANCELLED", err)
	}
	if lost.Holder != "" {
		t.Errorf("Holder = %q, want nobody: a cancelled job has no worker", lost.Holder)
	}

	// Taken over by another node.
	a.Create(ctx, "taken")
	lt, _, _ := a.Claim(ctx, "node-a", time.Minute)
	clock.Advance(2 * time.Minute)
	b.Claim(ctx, "node-b", time.Minute)
	err = a.Transition(ctx, lt, alchemy.JobSucceeded)
	if !errors.As(err, &lost) || lost.Holder != "node-b" {
		t.Errorf("err = %v, want a LeaseError naming node-b as the holder", err)
	}
	if lost.State != alchemy.JobRunning {
		t.Errorf("State = %q, want RUNNING under its new owner", lost.State)
	}

	// Reaped out from under it entirely.
	a.Create(ctx, "reaped")
	lr, _, _ := a.Claim(ctx, "node-a", time.Minute)
	a.Cancel(ctx, "reaped")
	clock.Advance(2 * time.Minute)
	if swept, _ := b.Expire(ctx); !has(swept.Reaped, "reaped") {
		t.Fatal("the finished job was not dropped")
	}
	err = a.Transition(ctx, lr, alchemy.JobSucceeded)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for a job that is gone", err)
	}
	if msg := err.Error(); !contains(msg, "reaped") && !contains(msg, "no longer") {
		t.Errorf("err = %q, want it to say the job is gone rather than merely refused", msg)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Two clients retrying the same nightly import at the same moment against two
// different nodes. Exactly one job exists afterwards, every caller is told
// about the same one, and nobody is told the store is full.
func TestConcurrentCreateWithOneIdempotencyKeyAdmitsOneJob(t *testing.T) {
	a, b, _ := twoNodes(t, Config{Capacity: 64, PendingTTL: time.Hour})
	ctx := context.Background()

	const callers = 16
	var (
		mu       sync.Mutex
		admitted int
		jobs     []alchemy.Job
		wg       sync.WaitGroup
	)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			j, err := s.Create(ctx, "nightly-2026-08-30")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, ErrExists):
			default:
				t.Errorf("create: %v, want nil or ErrExists", err)
				return
			}
			jobs = append(jobs, j)
		}()
	}
	close(start)
	wg.Wait()

	if admitted != 1 {
		t.Errorf("%d callers were told they admitted the job; exactly one did", admitted)
	}
	if len(jobs) != callers {
		t.Fatalf("%d of %d callers got an answer", len(jobs), callers)
	}
	// Every caller must be handed the same job, or the retry path is a second
	// round trip through Get for whoever lost.
	for i, j := range jobs {
		if !sameJob(j, jobs[0]) {
			t.Errorf("caller %d got %+v, want the one admitted job %+v", i, j, jobs[0])
		}
	}
	var rows int
	if err := a.pool.QueryRow(ctx, a.q(`SELECT count(*) FROM {s}.jobs`)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d rows for one idempotency key", rows)
	}
}

// §8.4's admission control, once the queue is shared: the number is the
// cluster's, not the node's. Two nodes configured with a capacity of two hold
// two jobs between them, not four.
//
// That is a real change in meaning and it is the honest one. The in-memory
// store's capacity bounded what one process had to hold in its own heap; here
// the queue is in Postgres and what the limit protects is the shared backlog,
// so a per-node reading would let a ten-node cluster accept ten times the
// depth its operator asked for. Every node must be given the same number; a
// node configured lower simply refuses earlier than its neighbours.
func TestCapacityIsTheClustersAndNotEachNodes(t *testing.T) {
	a, b, _ := twoNodes(t, Config{Capacity: 2})
	ctx := context.Background()

	if _, err := a.Create(ctx, "one"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := b.Create(ctx, "two"); err != nil {
		t.Fatalf("second, on the other node: %v", err)
	}
	_, err := b.Create(ctx, "three")
	if !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("err = %v, want the second node to see the first node's jobs", err)
	}
	var full *CapacityError
	if !errors.As(err, &full) || full.Live != 2 || full.Capacity != 2 {
		t.Errorf("err = %v, want the numbers to describe the cluster", err)
	}
	if _, err := a.Create(ctx, "three"); !errors.Is(err, ErrAtCapacity) {
		t.Errorf("the node that admitted the first job still had room: %v", err)
	}
}
