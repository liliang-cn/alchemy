package budget_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/liliang-cn/alchemy/pkg/budget"
)

// newShared builds a Postgres budget on its own pool, or fails the test.
func newShared(t *testing.T, pool *pgxpool.Pool, cfg budget.PostgresConfig) *budget.Postgres {
	t.Helper()
	b, err := budget.NewPostgres(context.Background(), pool, cfg)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

// awaitSharedWaiters blocks until n callers are queued for model. Waiting is a
// query against the shared store now, so it can fail, and a failure here is a
// broken test rather than a broken budget.
func awaitSharedWaiters(t *testing.T, b *budget.Postgres, model string, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, err := b.Waiting(context.Background(), model)
		if err != nil {
			t.Fatalf("Waiting: %v", err)
		}
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d waiters on %q, have %d", n, model, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestNewPostgresRejectsABudgetWithNoBound(t *testing.T) {
	pg := newPG(t)
	pool := pg.pool()
	if _, err := budget.NewPostgres(context.Background(), pool, budget.PostgresConfig{Limit: 0}); err == nil {
		t.Fatal("want an error for Limit 0: a budget with no bound is not a budget")
	}
	if _, err := budget.NewPostgres(context.Background(), pool, budget.PostgresConfig{
		Limit: 4, PerModel: map[string]int{"gpt": -1},
	}); err == nil {
		t.Fatal("want an error for a negative per-model limit")
	}
}

// This is the test §8.2 is about: ten nodes each running "8 concurrent" is 80
// in flight against an endpoint that permits 20. Two budgets are two nodes;
// they share nothing but the database, and the number measured is the real
// high-water mark of overlapping holders across both, not a counter's opinion.
func TestTheBoundIsClusterWideAndNotPerNode(t *testing.T) {
	const (
		limit   = 2
		workers = 8
		rounds  = 3
		// The guarded section has to be long enough to be a model call. A
		// handover between nodes costs a notification and a transaction —
		// something under a millisecond on a loopback, minutes less than an
		// extraction — so a one-microsecond "call" would measure the store's
		// latency and conclude the budget was serial.
		call = 15 * time.Millisecond
	)
	pg := newPG(t)
	cfg := budget.PostgresConfig{Limit: limit, TTL: 10 * time.Second, Poll: 20 * time.Millisecond}
	nodeA := newShared(t, pg.pool(), cfg)
	nodeB := newShared(t, pg.pool(), cfg)

	// live[i] is how many holders node i has right now; overlapped records the
	// instant both nodes held one at once, which is the only arrangement that
	// makes this a cluster-wide measurement rather than two local ones.
	var p peak
	var live [2]atomic.Int64
	var overlapped atomic.Bool
	var wg sync.WaitGroup
	for n, node := range []*budget.Postgres{nodeA, nodeB} {
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(n int, b *budget.Postgres) {
				defer wg.Done()
				for r := 0; r < rounds; r++ {
					l, err := b.Acquire(context.Background(), "gpt")
					if err != nil {
						t.Errorf("Acquire: %v", err)
						return
					}
					p.enter()
					live[n].Add(1)
					runtime.Gosched()
					time.Sleep(call)
					if live[0].Load() > 0 && live[1].Load() > 0 {
						overlapped.Store(true)
					}
					live[n].Add(-1)
					p.leave()
					l.Release(nil)
				}
			}(n, node)
		}
	}
	wg.Wait()

	if !overlapped.Load() {
		t.Fatal("the two nodes never held slots at the same time: the bound was never tested across nodes")
	}

	if got := p.max.Load(); got > limit {
		t.Fatalf("peak concurrent holders across both nodes was %d, over the cluster budget of %d", got, limit)
	} else if got < limit {
		t.Fatalf("peak was only %d: the two nodes never overlapped, so the test proved nothing", got)
	}
	for name, b := range map[string]*budget.Postgres{"A": nodeA, "B": nodeB} {
		got, err := b.InFlight(context.Background(), "gpt")
		if err != nil {
			t.Fatalf("InFlight: %v", err)
		}
		if got != 0 {
			t.Fatalf("node %s sees %d slots still held after every Release", name, got)
		}
	}
}

// The readable test above proves two nodes share one bound. This one proves the
// mechanism that makes it true: forty-eight workers on three nodes, all woken
// by the same notification, all deciding at once whether there is room.
//
// It is here because the test above cannot fail for the right reason. Removing
// the advisory lock in attempt leaves it green — its handovers are too spread
// out to collide — while this one measured a peak of 3 against a limit of 2 in
// two runs out of four. A bound that is enforced by a lock needs a test that
// notices when the lock is gone.
func TestTheBoundHoldsWhenEveryNodeDecidesAtOnce(t *testing.T) {
	const (
		limit   = 2
		nodes   = 3
		workers = 16
		rounds  = 5
	)
	pg := newPG(t)
	cfg := budget.PostgresConfig{Limit: limit, TTL: 10 * time.Second, Poll: 5 * time.Millisecond}
	cluster := make([]*budget.Postgres, nodes)
	for i := range cluster {
		cluster[i] = newShared(t, pg.pool(), cfg)
	}

	var p peak
	var wg sync.WaitGroup
	for _, node := range cluster {
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(b *budget.Postgres) {
				defer wg.Done()
				for r := 0; r < rounds; r++ {
					l, err := b.Acquire(context.Background(), "gpt")
					if err != nil {
						t.Errorf("Acquire: %v", err)
						return
					}
					p.enter()
					time.Sleep(time.Millisecond)
					p.leave()
					l.Release(nil)
				}
			}(node)
		}
	}
	wg.Wait()

	if got := p.max.Load(); got > limit {
		t.Fatalf("peak concurrent holders was %d against a cluster budget of %d", got, limit)
	} else if got < limit {
		t.Fatalf("peak was only %d: the bound was never approached", got)
	}
}

// §8.3: a node that dies holding a slot must not shrink the cluster's budget
// forever. Here the node does not merely die, it dies badly — the lease is
// never released and never heartbeaten, which is what a killed process leaves
// behind.
func TestASlotHeldByADeadNodeComesBackAfterItsTTL(t *testing.T) {
	pg := newPG(t)
	const ttl = 400 * time.Millisecond
	cfg := budget.PostgresConfig{Limit: 1, TTL: ttl, Poll: 20 * time.Millisecond}
	dead := newShared(t, pg.pool(), cfg)
	alive := newShared(t, pg.pool(), cfg)

	if _, err := dead.Acquire(context.Background(), "gpt"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// The dead node's lease is dropped on the floor: no Release, no heartbeat.

	start := time.Now()
	l, err := alive.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("the surviving node never got the dead node's slot: %v", err)
	}
	defer l.Release(nil)
	if waited := time.Since(start); waited < ttl/2 {
		t.Fatalf("the slot came back after %v, before the TTL of %v had run: it was never really held", waited, ttl)
	}
}

// The failure a naive TTL introduces, against a real database: a call that
// takes longer than one TTL must not lose its slot. A ninety-second OCR call on
// a thirty-second TTL is the shape of this, compressed.
func TestASlowCallOutlivesSeveralTTLsAndKeepsItsSlot(t *testing.T) {
	pg := newPG(t)
	const ttl = 300 * time.Millisecond
	cfg := budget.PostgresConfig{Limit: 1, TTL: ttl, Poll: 20 * time.Millisecond}
	worker := newShared(t, pg.pool(), cfg)
	other := newShared(t, pg.pool(), cfg)

	lease, err := worker.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	call, stop := budget.Keepalive(context.Background(), lease)

	// The "call" runs for three TTLs. Nothing else may get the slot in that
	// time, and the call itself must not be cancelled.
	time.Sleep(3 * ttl)
	if call.Err() != nil {
		t.Fatalf("the slow call was cancelled while it was heartbeating: %v", context.Cause(call))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := other.Acquire(ctx, "gpt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("another node took the slot from a call that was still heartbeating (err=%v)", err)
	}

	stop()
	lease.Release(nil)
	took, err := other.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("the slot was not free after Release: %v", err)
	}
	took.Release(nil)
}

// Ticket order is what the shared queue promises, and this is the promise
// stated exactly: waiters whose tickets were taken one after another — each
// parked before the next arrives — are served in that order.
func TestWaitersAreServedInTicketOrder(t *testing.T) {
	pg := newPG(t)
	cfg := budget.PostgresConfig{Limit: 1, TTL: 10 * time.Second, Poll: 20 * time.Millisecond}
	b := newShared(t, pg.pool(), cfg)
	ctx := context.Background()

	held, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	const waiters = 5
	order := make(chan int, waiters)
	leases := make(chan budget.Lease, waiters)
	for i := 0; i < waiters; i++ {
		go func(i int) {
			l, err := b.Acquire(ctx, "gpt")
			if err != nil {
				t.Errorf("waiter %d: %v", i, err)
				return
			}
			order <- i
			leases <- l
		}(i)
		awaitSharedWaiters(t, b, "gpt", i+1)
	}

	held.Release(nil)
	for want := 0; want < waiters; want++ {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("waiter %d was served in position %d: the ticket order was not honoured", got, want)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("waiter %d never ran", want)
		}
		(<-leases).Release(nil)
	}
}

// A caller that gives up must take its ticket with it, or the queue fills with
// positions nobody is waiting in and every later waiter is held behind a ghost.
func TestAGivenUpWaiterLeavesNoTicketBehind(t *testing.T) {
	pg := newPG(t)
	cfg := budget.PostgresConfig{Limit: 1, TTL: 10 * time.Second, Poll: 20 * time.Millisecond}
	b := newShared(t, pg.pool(), cfg)

	held, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held.Release(nil)

	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan error, 1)
	go func() {
		_, err := b.Acquire(ctx, "gpt")
		gone <- err
	}()
	awaitSharedWaiters(t, b, "gpt", 1)
	cancel()
	if err := <-gone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire = %v, want context.Canceled", err)
	}
	awaitSharedWaiters(t, b, "gpt", 0)
}

// §8.2: backoff is coordinated through the same lease rather than chosen
// independently by each node. One node's 429 must close the endpoint for a node
// that never saw it — and the deadline is the database's clock, so two nodes
// with skewed clocks still agree about which round they are in.
func TestARateLimitOnOneNodeClosesTheEndpointForAnother(t *testing.T) {
	pg := newPG(t)
	cfg := budget.PostgresConfig{
		Limit:   2,
		TTL:     10 * time.Second,
		Poll:    20 * time.Millisecond,
		Backoff: budget.Backoff{Base: 700 * time.Millisecond, Max: 5 * time.Second, Jitter: 0},
	}
	limited := newShared(t, pg.pool(), cfg)
	innocent := newShared(t, pg.pool(), cfg)

	l, err := limited.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release(budget.TooFast(errors.New("HTTP 429"), 0))

	start := time.Now()
	next, err := innocent.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire on the innocent node: %v", err)
	}
	defer next.Release(nil)
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("the second node waited %v after another node's 429; the endpoint was not closed for it", waited)
	}
}

// The endpoint's own Retry-After beats our schedule when it is longer: it knows
// its window and we are guessing about it.
func TestTheEndpointsOwnRetryAfterIsHonoured(t *testing.T) {
	pg := newPG(t)
	cfg := budget.PostgresConfig{
		Limit:   1,
		TTL:     10 * time.Second,
		Poll:    20 * time.Millisecond,
		Backoff: budget.Backoff{Base: 10 * time.Millisecond, Max: 5 * time.Second, Jitter: 0},
	}
	b := newShared(t, pg.pool(), cfg)

	l, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release(budget.TooFast(errors.New("HTTP 429"), 800*time.Millisecond))

	start := time.Now()
	next, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer next.Release(nil)
	if waited := time.Since(start); waited < 600*time.Millisecond {
		t.Fatalf("waited %v, want the endpoint's own 800ms Retry-After", waited)
	}
}

func TestASecondReleaseIsNotASecondSlot(t *testing.T) {
	pg := newPG(t)
	cfg := budget.PostgresConfig{Limit: 2, TTL: 10 * time.Second, Poll: 20 * time.Millisecond}
	b := newShared(t, pg.pool(), cfg)
	ctx := context.Background()

	one, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	two, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	one.Release(nil)
	one.Release(nil)
	if got, err := b.InFlight(ctx, "gpt"); err != nil || got != 1 {
		t.Fatalf("InFlight = %d (err %v) after a doubled Release, want 1", got, err)
	}
	two.Release(nil)
}

// A lease that was reclaimed must say so rather than pretend, because the node
// holding it has to stop calling the endpoint.
func TestHeartbeatingAReclaimedSlotReportsItIsGone(t *testing.T) {
	pg := newPG(t)
	const ttl = 300 * time.Millisecond
	b := newShared(t, pg.pool(), budget.PostgresConfig{Limit: 1, TTL: ttl, Poll: 20 * time.Millisecond})

	l, err := b.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// No assertion to make: liveness is part of Lease, so a lease that did not
	// carry it would not have compiled.
	if got := l.TTL(); got != ttl {
		t.Fatalf("TTL = %v, want %v", got, ttl)
	}
	if err := l.Heartbeat(context.Background()); err != nil {
		t.Fatalf("Heartbeat on a healthy lease: %v", err)
	}

	// Let it lapse, then let another node reap it by asking for the slot.
	time.Sleep(ttl + 100*time.Millisecond)
	other := newShared(t, pg.pool(), budget.PostgresConfig{Limit: 1, TTL: ttl, Poll: 20 * time.Millisecond})
	taken, err := other.Acquire(context.Background(), "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer taken.Release(nil)

	if err := l.Heartbeat(context.Background()); !errors.Is(err, budget.ErrLeaseExpired) {
		t.Fatalf("Heartbeat on a reclaimed slot = %v, want ErrLeaseExpired", err)
	}
}
