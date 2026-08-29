package job

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// witness records what the store let happen, so the test can assert the one
// property that makes at-least-once safe (§8.3): a second writer loses
// harmlessly. "Harmlessly" is not a feeling — it is that no job is ever
// finished twice and no lease token is ever issued twice.
type witness struct {
	mu sync.Mutex
	// finished is keyed by the incarnation, not by the ID. A job ID outlives
	// the job — a client retries under the same name after the first was
	// collected and dropped — and counting by name would report that legal
	// second job as a corrupted first one.
	finished map[string]int
	tokens   map[uint64]bool
	// latest is the newest lease each incarnation has been claimed under. A
	// finish arriving under any older token is a node that was overtaken
	// stealing the completion from the node that actually did the work — which
	// the absorbing terminal states alone do not prevent, and the fence does.
	latest map[string]uint64
}

func incarnation(l Lease) string {
	return fmt.Sprintf("%s@%d", l.Job.ID, l.Job.CreatedAt.UnixNano())
}

func (w *witness) finish(t *testing.T, l Lease) {
	w.mu.Lock()
	defer w.mu.Unlock()
	who := incarnation(l)
	w.finished[who]++
	// Safe to compare: a claim can only be recorded before a successful finish
	// on the same incarnation, because after that finish the job is terminal
	// and nothing can claim it again.
	if got := w.latest[who]; got != l.token {
		t.Errorf("job %s finished under retired lease %d; %d holds it", who, l.token, got)
	}
}

func (w *witness) claimed(t *testing.T, l Lease) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tokens[l.token] {
		t.Errorf("token %d issued twice; two nodes hold the same fence", l.token)
	}
	w.tokens[l.token] = true
	w.latest[incarnation(l)] = l.token
}

func TestManyNodesOnOneStore(t *testing.T) {
	clock := NewManualClock(epoch)
	s := New(Config{
		Capacity:    600,
		PendingTTL:  time.Second,
		ReviewTTL:   2 * time.Second,
		ConflictTTL: 4 * time.Second,
		DoneTTL:     500 * time.Millisecond,
		Clock:       clock,
	})
	ctx := context.Background()
	w := &witness{finished: map[string]int{}, tokens: map[uint64]bool{}, latest: map[string]uint64{}}

	const jobs = 300
	ids := make([]string, jobs)
	for i := range ids {
		ids[i] = fmt.Sprintf("job-%03d", i)
	}

	// Two wait groups: the zombies below outlive the workers by construction —
	// they are woken by abandoned leases — so waiting for them in the same
	// group as the goroutines that close their exit channel is a deadlock.
	var wg, zombies sync.WaitGroup
	done := make(chan struct{})
	// stale carries leases that were deliberately abandoned, so the zombie
	// goroutines below can write under them long after they died — the case
	// §8.3 says must be survivable rather than prevented.
	stale := make(chan Lease, 64)

	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}

	// Producers. Two of them create the same IDs on purpose: idempotent
	// creation is only worth anything if it is safe when two clients retry at
	// once.
	for p := 0; p < 2; p++ {
		run(func() {
			for _, id := range ids {
				s.Create(ctx, id)
			}
		})
	}

	// Workers.
	for n := 0; n < 8; n++ {
		node := fmt.Sprintf("node-%d", n)
		run(func() {
			for i := 0; i < 400; i++ {
				l, ok, err := s.Claim(ctx, node, 5*time.Millisecond)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if !ok {
					continue
				}
				w.claimed(t, l)
				if l2, err := s.Heartbeat(ctx, l, "extract"); err == nil {
					l = l2
				}
				switch i % 4 {
				case 0:
					if err := s.Transition(ctx, l, alchemy.JobSucceeded); err == nil {
						w.finish(t, l)
					}
				case 1:
					if err := s.Fail(ctx, l, "synthetic"); err == nil {
						w.finish(t, l)
					}
				case 2:
					s.Hold(ctx, l, HoldReason(1+i%2))
				default:
					// Abandon the lease without releasing it: the node crashed.
					select {
					case stale <- l:
					default:
						s.Release(ctx, l)
					}
				}
			}
		})
	}

	// Zombies: nodes that come back from the dead and finish work somebody
	// else has since taken over and completed.
	for z := 0; z < 2; z++ {
		zombies.Add(1)
		go func() {
			defer zombies.Done()
			for {
				select {
				case l := <-stale:
					if err := s.Transition(ctx, l, alchemy.JobSucceeded); err == nil {
						w.finish(t, l)
					}
					if err := s.Fail(ctx, l, "zombie"); err == nil {
						w.finish(t, l)
					}
				case <-done:
					return
				}
			}
		}()
	}

	// Reviewers, answering whatever is held. Their successes are not counted:
	// a reviewer holds no lease, so there is no incarnation to attribute the
	// answer to, and what they are here to exercise is that answering a job a
	// worker is racing to expire corrupts nothing.
	for r := 0; r < 2; r++ {
		run(func() {
			for i := 0; i < 400; i++ {
				s.Resolve(ctx, ids[i%len(ids)], alchemy.JobSucceeded)
			}
		})
	}

	// The sweeper and the clock, which together make leases expire under the
	// workers rather than in a test that waits for them.
	run(func() {
		for i := 0; i < 500; i++ {
			if _, err := s.Expire(ctx); err != nil {
				t.Errorf("expire: %v", err)
				return
			}
		}
	})
	run(func() {
		for i := 0; i < 2000; i++ {
			clock.Advance(time.Millisecond)
		}
	})

	wg.Wait()
	close(done)
	zombies.Wait()

	w.mu.Lock()
	for who, n := range w.finished {
		if n > 1 {
			t.Errorf("job %s finished %d times; a late writer overwrote a finished job", who, n)
		}
	}
	w.mu.Unlock()

	// The live count is a counter rather than a scan, so the test that it never
	// drifts from the truth belongs here, where every path has been exercised.
	s.mu.Lock()
	defer s.mu.Unlock()
	want := 0
	for _, r := range s.jobs {
		if !terminal(r.job.State) {
			want++
		}
	}
	if s.live != want {
		t.Errorf("live = %d, but %d jobs are non-terminal; admission control is counting wrong", s.live, want)
	}
	if len(s.order) != len(s.jobs) {
		t.Errorf("order has %d ids for %d jobs", len(s.order), len(s.jobs))
	}
}
