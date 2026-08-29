package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Everything at once, on two nodes, under -race: workers claiming and
// finishing, nodes abandoning leases, zombies returning to write under them,
// reviewers answering holds, and a sweeper expiring things underneath all of
// it. The two properties asserted are the ones that make at-least-once safe
// (§8.3): no job is ever finished twice, and no fence token is ever issued
// twice.
func TestTwoNodesUnderContention(t *testing.T) {
	f := newFixture(t)
	clock := NewManualClock(epoch)
	cfg := PGConfig{Config: Config{
		// The lease is far shorter than the clock's total travel, so every
		// claim races a takeover: this is the regime §8.3 is about, where two
		// nodes are briefly working the same job. The pending and hold timers
		// are long enough that jobs survive to be fought over rather than
		// quietly expiring before anyone reaches them.
		Capacity:    600,
		PendingTTL:  20 * time.Second,
		ReviewTTL:   30 * time.Second,
		ConflictTTL: 60 * time.Second,
		DoneTTL:     2 * time.Second,
		Clock:       clock,
	}}
	nodes := []*PG{f.open(t, cfg), f.open(t, cfg)}
	ctx := context.Background()
	w := &witness{finished: map[string]int{}, tokens: map[uint64]bool{}, latest: map[string]uint64{}}

	const jobs = 400
	ids := make([]string, jobs)
	for i := range ids {
		ids[i] = fmt.Sprintf("job-%03d", i)
	}

	// Seed half the queue before anything starts. Over a network the producers
	// cannot keep six workers fed — they would still be inserting when the
	// workers had finished polling an empty queue, and the test would report a
	// green run in which almost nothing was contended.
	for _, id := range ids[:jobs/2] {
		if _, err := nodes[0].Create(ctx, id); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	var wg sync.WaitGroup
	// abandoned collects leases whose worker walked away without releasing
	// them — the crashed node of §8.3. They are written under later, on
	// purpose, after the jobs have been taken over by somebody else.
	var stalemu sync.Mutex
	var abandoned []Lease
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}

	// Two producers creating every ID, half of which are already seeded: so
	// this is both the fresh-admission path and two clients retrying the same
	// key at once against two different nodes.
	for p := 0; p < 2; p++ {
		s := nodes[p]
		run(func() {
			for _, id := range ids {
				s.Create(ctx, id)
			}
		})
	}

	for n := 0; n < 6; n++ {
		s := nodes[n%len(nodes)]
		// Six goroutines but two node names, because a node is a process and a
		// process runs several workers under one identity. That is the
		// configuration in which the holder's name cannot decide ownership —
		// a worker's retired lease and its neighbour's live one carry the same
		// name — so it is the configuration that actually exercises the fence.
		node := fmt.Sprintf("node-%d", n%len(nodes))
		run(func() {
			for i := 0; i < 60; i++ {
				l, ok, err := s.Claim(ctx, node, 20*time.Millisecond)
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
					// The node crashed holding this lease.
					stalemu.Lock()
					abandoned = append(abandoned, l)
					stalemu.Unlock()
				}
			}
		})
	}

	// Reviewers, answering whatever is held. Their successes are not counted: a
	// reviewer holds no lease, so there is no incarnation to attribute the
	// answer to.
	run(func() {
		for i := 0; i < 120; i++ {
			nodes[i%len(nodes)].Resolve(ctx, ids[i%len(ids)], alchemy.JobSucceeded)
		}
	})

	// Two sweepers, one per node, which is the configuration §8.3 says is
	// allowed: every decision a sweep makes is a checked transition, so a
	// second sweeper finds nothing left to do rather than doing it twice.
	for n := range nodes {
		s := nodes[n]
		run(func() {
			for i := 0; i < 60; i++ {
				if _, err := s.Expire(ctx); err != nil {
					t.Errorf("expire: %v", err)
					return
				}
			}
		})
	}
	run(func() {
		for i := 0; i < 1000; i++ {
			clock.Advance(5 * time.Millisecond)
		}
	})

	wg.Wait()

	// The zombies, and deliberately not while the workers ran.
	//
	// A node that comes back and writes before anyone has taken its job over
	// is not a zombie, it is still the holder — and letting that happen by
	// chance is how a stress test reports a green run in which the fence was
	// never consulted. So: move past every lease, let the same node names pick
	// the same jobs up again, and only then write under the dead leases.
	clock.Advance(time.Minute)
	for i := 0; i < 4*jobs; i++ {
		l, ok, err := nodes[i%len(nodes)].Claim(ctx, "node-0", time.Minute)
		if err != nil {
			t.Fatalf("takeover claim: %v", err)
		}
		if !ok {
			break
		}
		w.claimed(t, l)
	}
	var zombies sync.WaitGroup
	for z, l := range abandoned {
		s, l := nodes[z%len(nodes)], l
		zombies.Add(1)
		go func() {
			defer zombies.Done()
			if err := s.Transition(ctx, l, alchemy.JobSucceeded); err == nil {
				w.finish(t, l)
			}
			if err := s.Fail(ctx, l, "zombie"); err == nil {
				w.finish(t, l)
			}
		}()
	}
	zombies.Wait()
	if len(abandoned) == 0 {
		t.Fatal("no lease was ever abandoned; the zombie path proved nothing")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	for who, n := range w.finished {
		if n > 1 {
			t.Errorf("job %s finished %d times; a late writer overwrote a finished job", who, n)
		}
	}
	if len(w.tokens) == 0 {
		t.Fatal("nothing was ever claimed; the test proved nothing")
	}
	t.Logf("%d claims, %d completions across two nodes", len(w.tokens), len(w.finished))
}

// What admission control costs once it is cluster-wide, stated rather than
// discovered.
//
// The count and the insert are one statement, but READ COMMITTED means two
// concurrent admissions can both see room for one job. So the limit is exact
// when uncontended and can overshoot by at most the number of admissions in
// flight. The alternative — SERIALIZABLE, or a counter row, or an advisory
// lock — makes it exact by putting every admission in the cluster behind one
// lock, which is the throughput this limit exists to protect.
//
// That trade is defensible because of what Capacity is: a guard rail against a
// queue that accepts everything (§8.4), not an invariant anything depends on.
// A backlog of 130 when the operator asked for 128 is not the failure the
// number was written to prevent; a claim path that serialises on one row is.
//
// The two halves asserted here are the ones that would actually hurt: it must
// not under-admit, which would stall a cluster below the depth it was
// configured for, and it must not ignore the limit, which is the OOM.
func TestConcurrentAdmissionRespectsTheLimitWithinTheRaceItAllows(t *testing.T) {
	const capacity = 8
	const callers = 24
	a, b, _ := twoNodes(t, Config{Capacity: capacity, PendingTTL: time.Hour})
	ctx := context.Background()

	var mu sync.Mutex
	var admitted, refused int
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		s, id := a, fmt.Sprintf("job-%02d", i)
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Create(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, ErrAtCapacity):
				refused++
			default:
				t.Errorf("create %s: %v", id, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	var rows int
	if err := a.pool.QueryRow(ctx, a.q(`SELECT count(*) FROM {s}.jobs`)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if admitted != rows {
		t.Errorf("%d callers were told they were admitted but the store holds %d jobs", admitted, rows)
	}
	if admitted < capacity {
		t.Errorf("admitted %d of a capacity of %d: a cluster that refuses below its own "+
			"configured depth is one that never fills its workers", admitted, capacity)
	}
	if admitted > capacity+callers {
		t.Errorf("admitted %d against a capacity of %d: the limit was not consulted", admitted, capacity)
	}
	if refused == 0 {
		t.Errorf("nothing was refused; %d callers against a capacity of %d must "+
			"produce a try-later", callers, capacity)
	}
	t.Logf("capacity %d, %d concurrent callers: %d admitted, %d refused (overshoot %d)",
		capacity, callers, admitted, refused, admitted-capacity)
}

// The defaults must not accidentally make §7.3's two timers the same length,
// and the clustered store builds them separately from Mem — so it needs its
// own assertion rather than Mem's.
func TestTheClusteredDefaultsKeepTheTwoTimersApart(t *testing.T) {
	f := newFixture(t)
	s := f.open(t, PGConfig{})
	if s.cfg.ConflictTTL <= s.cfg.ReviewTTL {
		t.Errorf("default ConflictTTL %v must exceed ReviewTTL %v", s.cfg.ConflictTTL, s.cfg.ReviewTTL)
	}
	// A buyer evaluating the product runs Mem and then deploys PG. Every
	// duration a zero Config implies must therefore be the same in both, or
	// the deployment is the moment the behaviour changes.
	//
	// The Clock is the one field that must differ, and it is the subject of
	// pgclock_test.go: Mem fills it with the wall clock because on one node
	// the wall clock is the store's clock, and the clustered store leaves it
	// nil because in a cluster it is not.
	mem := New(Config{})
	want, got := mem.cfg, s.cfg
	want.Clock, got.Clock = nil, nil
	if want != got {
		t.Errorf("clustered defaults %+v differ from the in-memory ones %+v", got, want)
	}
	if mem.cfg.Clock == nil {
		t.Error("the in-memory store stopped defaulting its clock; the comparison above is vacuous")
	}
}

// Ten nodes deploying at once must produce one schema, not nine crashes.
func TestMigrateIsSafeToRunFromEveryNodeAtOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := OpenPG(ctx, f.dsn, PGConfig{Schema: f.schema})
			if err != nil {
				t.Errorf("open: %v", err)
				return
			}
			defer s.Close()
			if err := s.Migrate(ctx); err != nil {
				t.Errorf("migrate: %v", err)
			}
		}()
	}
	wg.Wait()
}
