package job

import (
	"context"
	"sync"
	"testing"
	"time"
)

// FIFO, stated as two tests because it is two different promises.
//
// The in-memory store makes one promise: oldest first, always, because it
// holds a mutex and a slice. SKIP LOCKED cannot make that promise to N nodes
// at once and it would be dishonest to keep asserting it — so this file says
// what the clustered store actually guarantees and tests exactly that.

// Uncontended, the promise is unchanged and is asserted at full strength: a
// single worker draining a queue sees it in arrival order, every time. This is
// the case a single-node deployment and every operator's mental model live in.
func TestOneWorkerDrainsTheQueueInArrivalOrder(t *testing.T) {
	f := newFixture(t)
	s, _ := f.store(t, Config{})
	ctx := context.Background()

	const jobs = 25
	want := make([]string, jobs)
	for i := range want {
		want[i] = string(rune('a' + i%26))
		want[i] = want[i] + string(rune('0'+i/26)) + "-" + string(rune('A'+i))
		if _, err := s.Create(ctx, want[i]); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	for i, id := range want {
		l, ok, err := s.Claim(ctx, "node-a", time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
		}
		if l.Job.ID != id {
			t.Fatalf("claim %d took %q, want %q: the queue is not in arrival order",
				i, l.Job.ID, id)
		}
	}
}

// Contended, the promise is weaker and is written down here rather than left
// to be discovered: N nodes claiming at once take the N oldest jobs, but which
// node gets which is unordered.
//
// That is what SKIP LOCKED buys and what it costs. A claimer that finds the
// oldest job locked by another claimer moves to the next rather than waiting,
// so the assignment is not a total order — but nothing is starved, because a
// job is only ever passed over while N-1 older jobs are being claimed at that
// instant, never because it is unlucky. Dropping SKIP LOCKED would restore
// strict order by making every node in the cluster queue behind whichever one
// is slowest, which is a worse thing to have promised.
func TestConcurrentClaimersTakeTheOldestJobsButNotInOrder(t *testing.T) {
	f := newFixture(t)
	s, _ := f.store(t, Config{Capacity: 200})
	ctx := context.Background()

	const nodes = 8
	const jobs = 40
	ids := make([]string, jobs)
	for i := range ids {
		ids[i] = "job-" + string(rune('A'+i/10)) + string(rune('0'+i%10))
		if _, err := s.Create(ctx, ids[i]); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	oldest := map[string]bool{}
	for _, id := range ids[:nodes] {
		oldest[id] = true
	}

	var mu sync.Mutex
	got := map[string]string{} // job -> node
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := 0; n < nodes; n++ {
		node := "node-" + string(rune('a'+n))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l, ok, err := s.Claim(ctx, node, time.Minute)
			if err != nil {
				t.Errorf("%s claim: %v", node, err)
				return
			}
			if !ok {
				t.Errorf("%s got nothing from a queue of %d", node, jobs)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if other, dup := got[l.Job.ID]; dup {
				t.Errorf("%s and %s both claimed %s", other, node, l.Job.ID)
			}
			got[l.Job.ID] = node
		}()
	}
	close(start)
	wg.Wait()

	if len(got) != nodes {
		t.Fatalf("%d jobs claimed by %d nodes", len(got), nodes)
	}
	// The set is FIFO even though the assignment is not. A claimer can only be
	// pushed past the head of the queue by another claimer holding it, so with
	// N claimers nothing below the Nth oldest is reachable.
	for id := range got {
		if !oldest[id] {
			t.Errorf("claimed %q, which is not among the %d oldest: a claimer "+
				"skipped past rows nobody was holding", id, nodes)
		}
	}
}

// The sweep must not hold one transaction across the whole backlog.
//
// This asserts the mechanism and not just the result, because the result is
// identical either way: a single unbatched UPDATE also expires all five jobs.
// What it cannot do is take three statements to do it. On a table with 200k
// expired rows the difference between those two implementations is a sweeper
// that pins the xmin horizon for minutes and one that does not.
func TestTheSweepIsBatchedRatherThanOneLongTransaction(t *testing.T) {
	f := newFixture(t)
	clock := NewManualClock(epoch)
	s := f.open(t, PGConfig{
		Config:     Config{PendingTTL: time.Minute, DoneTTL: time.Hour, Clock: clock},
		SweepBatch: 2,
	})
	ctx := context.Background()

	const jobs = 5
	for i := 0; i < jobs; i++ {
		if _, err := s.Create(ctx, "queued-"+string(rune('a'+i))); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	clock.Advance(time.Minute)

	before := s.sweeps.Load()
	swept, err := s.Expire(ctx)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(swept.Expired) != jobs {
		t.Fatalf("expired %d of %d jobs: a batched sweep must still finish the pass",
			len(swept.Expired), jobs)
	}
	// One requeue statement (nothing to do), three expire statements for five
	// rows at two a time, one reap statement (the rows it would want are all
	// freshly expired and still collectable).
	if got, want := s.sweeps.Load()-before, int64(5); got != want {
		t.Errorf("one pass took %d statements, want %d: %d rows at a batch of 2 "+
			"cannot be one statement", got, want, jobs)
	}
}
