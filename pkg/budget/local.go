package budget

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Config declares a budget. Limit is per endpoint, never a total: two models
// are two rate limits and sharing one number between them would throttle the
// cheap one to protect the expensive one.
type Config struct {
	// Limit is how many calls may be in flight against one model endpoint.
	Limit int
	// PerModel overrides Limit for named endpoints, keyed by the model name the
	// caller passes to Acquire.
	PerModel map[string]int
	// Backoff is how long an endpoint stays closed after it rate-limits a call.
	// The zero value is a usable policy.
	Backoff Backoff
	// Clock defaults to SystemClock. It exists so a test can move a backoff
	// deadline rather than sleep through it.
	Clock Clock
	// Rand draws the jitter, in [0,1). It defaults to math/rand/v2, and is
	// injectable for the same reason as Clock.
	Rand func() float64
}

// Local implements Budget, and its lease implements Lease — including the
// liveness half, which it answers in the only way an in-process slot can.
var (
	_ Budget = (*Local)(nil)
	_ Lease  = (*lease)(nil)
)

// Local is an in-process Budget: the default, and what a single node runs
// (§8.3). It is safe for concurrent use.
type Local struct {
	cfg Config

	mu        sync.Mutex
	endpoints map[string]*endpoint
}

// NewLocal validates the configuration and returns the budget. A non-positive
// limit is refused rather than treated as "unlimited": a budget with no bound
// is not a budget, and the failure it would cause — 429s under load — surfaces
// far from the line that misconfigured it.
func NewLocal(cfg Config) (*Local, error) {
	if cfg.Limit <= 0 {
		return nil, errors.New("budget: Config.Limit must be positive; a budget with no bound is not a budget")
	}
	for model, n := range cfg.PerModel {
		if n <= 0 {
			return nil, fmt.Errorf("budget: Config.PerModel[%q] is %d; a per-endpoint limit must be positive", model, n)
		}
	}
	copied := cfg
	copied.PerModel = nil
	if copied.Clock == nil {
		copied.Clock = SystemClock{}
	}
	if copied.Rand == nil {
		copied.Rand = defaultDraw
	}
	if len(cfg.PerModel) > 0 {
		// Copied so a caller mutating the map later cannot change a live bound.
		copied.PerModel = make(map[string]int, len(cfg.PerModel))
		for k, v := range cfg.PerModel {
			copied.PerModel[k] = v
		}
	}
	return &Local{cfg: copied, endpoints: map[string]*endpoint{}}, nil
}

// Waiting reports how many callers are parked in Acquire for model. It is an
// operator number — a queue that only grows is the signal that the endpoint,
// not the node, is the bottleneck (§8.2).
func (l *Local) Waiting(model string) int {
	e := l.endpointFor(model)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.queue.Len()
}

// InFlight reports how many slots for model are currently held.
func (l *Local) InFlight(model string) int {
	e := l.endpointFor(model)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inUse
}

func (l *Local) limitFor(model string) int {
	if n, ok := l.cfg.PerModel[model]; ok {
		return n
	}
	return l.cfg.Limit
}

func (l *Local) endpointFor(model string) *endpoint {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.endpoints[model]
	if !ok {
		e = &endpoint{
			limit:   l.limitFor(model),
			queue:   list.New(),
			policy:  l.cfg.Backoff,
			clock:   l.cfg.Clock,
			draw:    l.cfg.Rand,
			maxWait: l.cfg.Backoff.normalised().Max,
		}
		l.endpoints[model] = e
	}
	return e
}

// Acquire implements Budget.
func (l *Local) Acquire(ctx context.Context, model string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		// A caller whose context is already dead must not consume a slot it
		// cannot use, even when one is free.
		return nil, err
	}
	e := l.endpointFor(model)
	if err := e.acquire(ctx); err != nil {
		return nil, err
	}
	return &lease{e: e}, nil
}

// endpoint is one model's bound: the slots, and the queue of callers waiting
// for one.
type endpoint struct {
	limit  int
	policy Backoff
	clock  Clock
	draw   func() float64
	// maxWait is the normalised cap, kept so an endpoint's own Retry-After is
	// bounded by the same number the schedule is.
	maxWait time.Duration

	mu    sync.Mutex
	inUse int
	// queue holds *waiter in arrival order. Ordering is the point: see the FIFO
	// argument on acquire.
	queue *list.List
	// attempts counts rounds of backoff, not refusals: see penalise.
	attempts int
	// until is when the endpoint may be called again. It is endpoint state, not
	// caller state, which is what makes the backoff coordinated (§8.2).
	until time.Time
}

// waiter is one parked caller. ch is buffered so a releaser can hand the slot
// over while holding the endpoint lock without ever blocking on a caller that
// may already have given up.
type waiter struct{ ch chan struct{} }

// acquire takes a slot, blocking in arrival order.
//
// The queue is FIFO, and that is a promise rather than an accident. A buffered
// channel used as a semaphore hands the slot to whichever goroutine the
// runtime happens to wake, which under sustained load lets one chunk sit behind
// an unbounded stream of newcomers — and a job is only finished when its last
// chunk is. Bounded waiting is what makes "a node that cannot get a slot waits"
// a safe instruction.
func (e *endpoint) acquire(ctx context.Context) error {
	e.mu.Lock()
	if e.inUse < e.limit && e.queue.Len() == 0 {
		e.inUse++
		e.mu.Unlock()
		return e.waitOutBackoff(ctx)
	}
	w := &waiter{ch: make(chan struct{}, 1)}
	el := e.queue.PushBack(w)
	e.mu.Unlock()

	select {
	case <-w.ch:
		// The releaser handed the slot straight to us; inUse still counts it.
		return e.waitOutBackoff(ctx)
	case <-ctx.Done():
		e.mu.Lock()
		select {
		case <-w.ch:
			// Handed the slot in the same moment we gave up. Holding the lock
			// is not enough to prevent that, so the slot has to be passed on
			// rather than dropped, or the bound shrinks by one forever.
			e.mu.Unlock()
			e.releaseSlot()
		default:
			e.queue.Remove(el)
			e.mu.Unlock()
		}
		return ctx.Err()
	}
}

// waitOutBackoff blocks until the endpoint may be called again, holding the
// slot while it does.
//
// The slot is held rather than given back on purpose. During backoff nobody is
// calling the endpoint, so an idle held slot costs nothing, and handing it back
// would put a caller who has already waited its turn to the back of the queue —
// the starvation the FIFO queue exists to prevent, reintroduced by the retry
// path. It also means at most `limit` calls leave when the deadline passes,
// which is what keeps the resumption from being a second spike.
func (e *endpoint) waitOutBackoff(ctx context.Context) error {
	for {
		e.mu.Lock()
		wait := e.until.Sub(e.clock.Now())
		e.mu.Unlock()
		if wait <= 0 {
			return nil
		}
		timer := e.clock.NewTimer(wait)
		select {
		case <-timer.C():
			// Loop rather than return: another worker may have been refused
			// while we waited, and the endpoint's deadline has moved.
		case <-ctx.Done():
			timer.Stop()
			e.releaseSlot()
			return ctx.Err()
		}
	}
}

// penalise closes the endpoint after a rate limit.
//
// A report that arrives while the endpoint is already closed is folded into the
// round in progress: it neither escalates nor extends. Every call that was in
// flight when the endpoint started refusing will come back 429, and treating
// twenty reports of one refusal as twenty rounds takes the delay to the cap on
// the first incident — the cluster punishing itself for its own concurrency
// rather than for the endpoint's answer. One round per refusal we actually
// caused is the honest count.
func (e *endpoint) penalise(retryAfter time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock.Now()
	if now.Before(e.until) {
		return
	}
	e.attempts++
	wait := e.policy.Delay(e.attempts, e.draw)
	if retryAfter > wait {
		// The endpoint knows its own window; our schedule is a guess about it.
		wait = retryAfter
	}
	if wait > e.maxWait {
		// Bounded even so: a header asking for a day is either a bug or a
		// decision an operator should be making, not one a stalled job makes
		// silently. The next call after the cap simply earns another round.
		wait = e.maxWait
	}
	e.until = now.Add(wait)
}

// recovered records that the endpoint served a call. A success while a backoff
// is still running proves nothing — it is a call that was already in flight —
// so only a success after the deadline resets the schedule.
func (e *endpoint) recovered() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.clock.Now().Before(e.until) {
		e.attempts = 0
	}
}

// releaseSlot returns one slot, handing it directly to the longest-waiting
// caller when there is one. The count is not decremented in that case: the slot
// never becomes free, it changes owner, which is what keeps a newcomer from
// overtaking the queue.
func (e *endpoint) releaseSlot() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if el := e.queue.Front(); el != nil {
		e.queue.Remove(el)
		el.Value.(*waiter).ch <- struct{}{}
		return
	}
	e.inUse--
}

type lease struct {
	e    *endpoint
	done atomic.Bool
}

// TTL is zero: an in-process slot cannot outlive the process that holds it.
// There is no reaper to race, because the memory holding the count and the
// goroutine holding the slot die in the same instant, so there is no interval
// at which "is this node still alive?" could be answered no from in here.
func (l *lease) TTL() time.Duration { return 0 }

// Heartbeat is a no-op — literally nothing, not a cheap update — for the same
// reason. It exists so that a caller holding a Lease never has to ask which
// implementation it got, and so Keepalive can short-circuit on the TTL instead
// of on a type switch over every budget that will ever exist.
func (l *lease) Heartbeat(context.Context) error {
	if l.done.Load() {
		// A released slot is no longer held, and saying otherwise would let a
		// caller keep working past its own Release.
		return ErrLeaseExpired
	}
	return nil
}

func (l *lease) Release(err error) {
	if !l.done.CompareAndSwap(false, true) {
		return // a second Release is a no-op, never a second slot.
	}
	// The outcome is recorded before the slot moves, so the next caller to be
	// handed it already sees the backoff this call just caused.
	switch retryAfter, limited := IsRateLimit(err); {
	case limited:
		l.e.penalise(retryAfter)
	case err == nil:
		l.e.recovered()
	}
	l.e.releaseSlot()
}
