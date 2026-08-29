package budget

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the shared Budget of §8.2: one bound per model endpoint, held in
// the database every node can see, so that ten nodes running "8 concurrent" are
// eight calls in flight and not eighty.
//
// It implements Budget, and the leases it hands out implement LiveLease: a slot
// is a row with an expiry, so a node that dies holding one gives it back after
// a TTL instead of shrinking the cluster's budget forever.
//
// What it does not promise, stated here rather than discovered in production:
//
//   - **FIFO is by ticket, and a ticket is taken when it commits.** A waiter
//     that cannot have a slot inserts a row whose sequence number is its place
//     in the queue, and no ticket taken later can overtake it. That is bounded
//     waiting, which is the property the local budget's strict FIFO was bought
//     for. What is weaker is the ordering of two callers that ask at the same
//     instant on different nodes: their order is the order their inserts
//     committed, which the database decides and no clock in this process can
//     observe. Local's queue orders by arrival at a mutex; this one orders by
//     arrival at the database, and those differ by a network hop.
//   - **A waiter that stops asking loses its place.** A ticket has its own
//     expiry, refreshed on every poll, because a queue that keeps the positions
//     of dead nodes is a queue that stops moving. So a node paused for longer
//     than TicketTTL — swapped out, stopped in a debugger, partitioned — can be
//     overtaken. Bounded waiting holds for waiters that are still waiting.
//   - **Waiting and InFlight are no longer O(1) and no longer infallible.**
//     They are indexed counts over the shared tables, one round trip each, and
//     they take a context and return an error because a number that has to
//     cross the network can fail to arrive.
type Postgres struct {
	pool *pgxpool.Pool
	cfg  PostgresConfig

	listener *pgListener

	closeOnce sync.Once
}

var _ Budget = (*Postgres)(nil)

// PostgresConfig declares a shared budget. Limit is per endpoint for the same
// reason it is in Config: two models are two rate limits.
type PostgresConfig struct {
	// Limit is how many calls may be in flight against one model endpoint,
	// across the whole cluster.
	Limit int
	// PerModel overrides Limit for named endpoints.
	PerModel map[string]int
	// Backoff is how long an endpoint stays closed after it rate-limits a call.
	// The zero value is a usable policy.
	Backoff Backoff
	// Rand draws the jitter, in [0,1). It defaults to math/rand/v2.
	Rand func() float64

	// TTL is how long a held slot survives with no heartbeat. It is the only
	// thing standing between a killed node and a permanently smaller cluster
	// budget, and it is also the deadline a slow call must keep beating — see
	// Keepalive, which renews three times per TTL.
	//
	// Default 30s. Longer means a dead node's slot is missing for longer;
	// shorter means less tolerance for a store that is briefly unreachable.
	TTL time.Duration
	// Poll is how often a waiter re-asks when no notification arrives.
	// Notifications make a handover prompt; polling is what makes it correct,
	// because a slot that expires produces no notification at all. Default
	// 250ms.
	Poll time.Duration
	// TicketTTL is how long a queue position survives without being refreshed.
	// Default is ten polls, and never less than one second.
	TicketTTL time.Duration
	// Node names this process in the slot table. It is diagnostic — the bound
	// is enforced by rows, not by names — and defaults to host/pid.
	Node string
	// Channel is the LISTEN/NOTIFY channel used to wake waiters promptly. It is
	// database-wide rather than schema-wide, so two deployments sharing one
	// database should give it two names; a notification from the wrong one is
	// harmless (it causes one extra re-check) but not free. Default
	// "alchemy_budget".
	Channel string
}

// Defaults, and the reasoning for the two that are not obvious. Poll at 250ms
// because the notification path carries the latency that matters and the poll
// is only there for expiries, which are measured in TTLs. A ticket living ten
// polls means a waiter has to miss ten consecutive attempts before the queue
// gives up on it, which is a partition rather than a hiccup.
const (
	defaultTTL     = 30 * time.Second
	defaultPoll    = 250 * time.Millisecond
	defaultChannel = "alchemy_budget"
	minTicketTTL   = time.Second

	// releaseTimeout bounds the work Release does. Release takes no context —
	// it is called from a defer on the way out of a model call, where the
	// caller's context may already be dead and the slot must still go back — so
	// it uses its own. If the store is unreachable for longer than this, the
	// slot is not leaked forever: it expires with the TTL, which is the whole
	// point of having one.
	releaseTimeout = 10 * time.Second
)

// NewPostgres validates the configuration, creates the tables if they are
// missing, and returns the budget. Close it when the process is done with it.
//
// The pool is the caller's: connection sizing, TLS and credentials are a
// deployment's business and not a budget's. It must allow at least a handful of
// connections — every waiter's re-check is a short transaction — and the
// listener does not take one from it, holding its own instead so that a
// saturated pool cannot cost the cluster its wakeups.
func NewPostgres(ctx context.Context, pool *pgxpool.Pool, cfg PostgresConfig) (*Postgres, error) {
	if pool == nil {
		return nil, errors.New("budget: NewPostgres needs a pool")
	}
	if cfg.Limit <= 0 {
		return nil, errors.New("budget: PostgresConfig.Limit must be positive; a budget with no bound is not a budget")
	}
	for model, n := range cfg.PerModel {
		if n <= 0 {
			return nil, fmt.Errorf("budget: PostgresConfig.PerModel[%q] is %d; a per-endpoint limit must be positive", model, n)
		}
	}
	copied := cfg
	copied.PerModel = nil
	if len(cfg.PerModel) > 0 {
		copied.PerModel = make(map[string]int, len(cfg.PerModel))
		for k, v := range cfg.PerModel {
			copied.PerModel[k] = v
		}
	}
	if copied.Rand == nil {
		copied.Rand = defaultDraw
	}
	if copied.TTL <= 0 {
		copied.TTL = defaultTTL
	}
	if copied.Poll <= 0 {
		copied.Poll = defaultPoll
	}
	if copied.TicketTTL <= 0 {
		copied.TicketTTL = 10 * copied.Poll
	}
	if copied.TicketTTL < minTicketTTL {
		copied.TicketTTL = minTicketTTL
	}
	if copied.Node == "" {
		host, err := os.Hostname()
		if err != nil {
			host = "unknown"
		}
		copied.Node = fmt.Sprintf("%s/%d", host, os.Getpid())
	}
	if copied.Channel == "" {
		copied.Channel = defaultChannel
	}
	if !plainIdentifier(copied.Channel) {
		// LISTEN cannot take a bind parameter, so the name is concatenated into
		// SQL and has to be proved safe rather than escaped hopefully.
		return nil, fmt.Errorf("budget: PostgresConfig.Channel %q must be a plain lower-case identifier", copied.Channel)
	}

	if err := migrate(ctx, pool); err != nil {
		return nil, err
	}
	p := &Postgres{pool: pool, cfg: copied}
	p.listener = newPGListener(pool, copied.Channel)
	return p, nil
}

// Close stops the listener. Held leases are not released: a slot belongs to the
// call that holds it, and a process shutting down mid-call would rather the
// endpoint stayed protected until the TTL says otherwise.
func (p *Postgres) Close() {
	p.closeOnce.Do(p.listener.close)
}

func (p *Postgres) limitFor(model string) int {
	if n, ok := p.cfg.PerModel[model]; ok {
		return n
	}
	return p.cfg.Limit
}

// Acquire implements Budget: it blocks until a slot for model is free across
// the cluster and the endpoint is not in backoff, or until ctx is done.
//
// The shape is a loop rather than a handoff, and that is the trade §8.2's
// shared store forces. In-process, a releaser can pass the slot straight to the
// longest-waiting goroutine; across nodes there is nobody to pass it to, so a
// waiter asks, is told no, and waits to be told something changed. The
// notification makes "something changed" prompt in the common case and the poll
// makes it certain in the uncommon one — a slot reclaimed from a dead node
// changes the answer without anybody sending anything.
func (p *Postgres) Acquire(ctx context.Context, model string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		// A caller whose context is already dead must not consume a slot it
		// cannot use, even when one is free.
		return nil, err
	}
	limit := p.limitFor(model)
	var ticket int64

	for {
		// Subscribed before asking, so a release that lands between the answer
		// and the wait is not a wakeup we slept through.
		changed := p.listener.watch(model)

		res, err := p.attempt(ctx, model, limit, &ticket)
		if err != nil {
			p.dropTicket(model, ticket)
			return nil, err
		}
		if res.granted {
			if res.wake {
				// Room is left and somebody is queued for it. The notification
				// is sent after the transaction that took this slot committed,
				// so the waiter it wakes sees the count it is about to act on.
				p.notify(ctx, model)
			}
			return &pgLease{p: p, id: res.leaseID, model: model}, nil
		}

		wait := p.cfg.Poll
		if res.backoff > 0 {
			// Waiting out the endpoint's own deadline rather than polling
			// through it. The ticket keeps the place in the queue meanwhile,
			// which is why this budget can afford to wait without a slot where
			// the local one holds its slot through a backoff.
			wait = res.backoff
		}
		if half := p.cfg.TicketTTL / 2; wait > half {
			// Never long enough to let our own ticket lapse: losing a queue
			// position to our own patience would be starvation we caused.
			wait = half
		}

		timer := time.NewTimer(wait)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			p.dropTicket(model, ticket)
			return nil, ctx.Err()
		}
	}
}

// dropTicket removes a queue position its owner has abandoned. It uses its own
// context because the caller's is typically the one that just expired, and a
// ticket left behind holds up every waiter that took a later one until it ages
// out — correct, but slow for a reason nobody would find.
func (p *Postgres) dropTicket(model string, ticket int64) {
	if ticket == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), releaseTimeout)
	defer cancel()
	_, _ = p.pool.Exec(ctx, sqlDropTicket, ticket)
	p.notify(ctx, model)
}

// InFlight reports how many slots for model are currently held across the
// cluster. Unlike Local's, it is a query: it takes a context and can fail.
func (p *Postgres) InFlight(ctx context.Context, model string) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, sqlCountSlots, model).Scan(&n)
	return n, err
}

// Waiting reports how many callers are queued for model across the cluster. It
// is the operator number §8.2 wants — a queue that only grows says the
// endpoint, not the node, is the bottleneck — and it counts tickets, so a
// waiter that has stopped refreshing is not counted as waiting.
func (p *Postgres) Waiting(ctx context.Context, model string) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, sqlCountTickets, model).Scan(&n)
	return n, err
}

// plainIdentifier reports whether s can be concatenated into SQL as an
// identifier without quoting or escaping.
func plainIdentifier(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case i > 0 && (r >= '0' && r <= '9'):
		default:
			return false
		}
	}
	return !strings.HasPrefix(s, "pg_")
}
