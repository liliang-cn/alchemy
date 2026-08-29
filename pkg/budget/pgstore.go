package budget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The tables. Three of them, and each is one sentence of §8.2 made durable:
// slot is "a call is in flight", ticket is "somebody is waiting and this is
// where in the queue", endpoint is "this endpoint said no and here is until
// when".
//
// Every deadline in them is a timestamptz written by now() on the server. That
// is the whole reason the coordination works: two nodes with skewed clocks
// disagree about whether a 429 belongs to the current round, and the only clock
// they can both be wrong against in the same direction is the database's.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS alchemy_budget_slot (
	id         text PRIMARY KEY,
	model      text NOT NULL,
	node       text NOT NULL,
	expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS alchemy_budget_slot_model_idx ON alchemy_budget_slot (model, expires_at);

CREATE TABLE IF NOT EXISTS alchemy_budget_ticket (
	seq        bigserial PRIMARY KEY,
	model      text NOT NULL,
	node       text NOT NULL,
	expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS alchemy_budget_ticket_model_idx ON alchemy_budget_ticket (model, seq);

CREATE TABLE IF NOT EXISTS alchemy_budget_endpoint (
	model    text PRIMARY KEY,
	until    timestamptz NOT NULL,
	attempts integer NOT NULL
);
`

// advisoryClass namespaces this package's advisory locks so that another
// component locking on the same hashed string in the same database cannot
// collide with a budget's.
const advisoryClass = int32(0x616C6362)

const (
	// sqlLock serialises every decision about one endpoint. Without it two
	// nodes both read "one slot in use, limit two" and both take the last one:
	// the bound would hold in the query and break in the endpoint.
	sqlLock = `SELECT pg_advisory_xact_lock($1, hashtext($2))`

	// sqlReap is a separate statement rather than a CTE on the count below,
	// because a data-modifying CTE is invisible to the rest of its own
	// statement — the counts would still see the rows it deleted, and the
	// budget would stay shrunk by every dead node it ever had.
	sqlReap = `
WITH dead_tickets AS (
	DELETE FROM alchemy_budget_ticket WHERE model = $1 AND expires_at <= now()
)
DELETE FROM alchemy_budget_slot WHERE model = $1 AND expires_at <= now()`

	// sqlTally is the whole decision in one round trip: how long the endpoint
	// is still closed for, how many slots are held, and how many waiters are
	// ahead of this one. $2 is this caller's ticket, or a sequence number
	// larger than any real one when it has none — a caller without a ticket is
	// behind every caller with one, which is what stops a newcomer overtaking
	// the queue.
	sqlTally = `
SELECT
	COALESCE((SELECT GREATEST(0, EXTRACT(EPOCH FROM (until - now())) * 1000)::bigint
	          FROM alchemy_budget_endpoint WHERE model = $1), 0),
	(SELECT count(*) FROM alchemy_budget_slot WHERE model = $1),
	(SELECT count(*) FROM alchemy_budget_ticket WHERE model = $1 AND seq < $2)`

	sqlTakeTicket    = `INSERT INTO alchemy_budget_ticket (model, node, expires_at) VALUES ($1, $2, now() + make_interval(secs => $3)) RETURNING seq`
	sqlRefreshTicket = `UPDATE alchemy_budget_ticket SET expires_at = now() + make_interval(secs => $2) WHERE seq = $1 RETURNING seq`
	sqlDropTicket    = `DELETE FROM alchemy_budget_ticket WHERE seq = $1`

	// sqlGrant takes the slot and gives up the queue position in one statement,
	// so there is no instant in which this caller holds both.
	sqlGrant = `
WITH used AS (
	DELETE FROM alchemy_budget_ticket WHERE seq = $4
)
INSERT INTO alchemy_budget_slot (id, model, node, expires_at)
VALUES ($1, $2, $3, now() + make_interval(secs => $5))`

	sqlHeartbeat = `UPDATE alchemy_budget_slot SET expires_at = now() + make_interval(secs => $2) WHERE id = $1 AND expires_at > now()`

	// sqlFree returns the slot and wakes the waiters in one statement, so a
	// crash cannot free a slot without telling anybody.
	sqlFree = `
WITH freed AS (
	DELETE FROM alchemy_budget_slot WHERE id = $1 RETURNING model
)
SELECT pg_notify($2, model) FROM freed`

	sqlNotify = `SELECT pg_notify($1, $2)`

	sqlCountSlots   = `SELECT count(*) FROM alchemy_budget_slot WHERE model = $1 AND expires_at > now()`
	sqlCountTickets = `SELECT count(*) FROM alchemy_budget_ticket WHERE model = $1 AND expires_at > now()`

	sqlEnsureEndpoint = `INSERT INTO alchemy_budget_endpoint (model, until, attempts) VALUES ($1, now(), 0) ON CONFLICT (model) DO NOTHING`
	sqlLockEndpoint   = `SELECT attempts, (until > now()) FROM alchemy_budget_endpoint WHERE model = $1 FOR UPDATE`
	sqlCloseEndpoint  = `UPDATE alchemy_budget_endpoint SET attempts = $2, until = now() + make_interval(secs => $3) WHERE model = $1`
	sqlRecovered      = `UPDATE alchemy_budget_endpoint SET attempts = 0 WHERE model = $1 AND until <= now()`
)

// noTicket is the sequence number a caller without a ticket compares against:
// larger than any bigserial will reach, so every real ticket counts as ahead.
const noTicket = int64(1<<63 - 1)

// migrate creates the tables. It runs under an advisory lock because two nodes
// starting at once both run CREATE TABLE IF NOT EXISTS, and Postgres answers
// that race with "tuple concurrently updated" rather than with a table.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlLock, advisoryClass, "alchemy_budget_schema"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schemaSQL)
		return err
	})
}

// attemptResult is one round of asking. backoff is how long the endpoint says
// it is still closed for, as the server measured it.
type attemptResult struct {
	granted bool
	leaseID string
	backoff time.Duration
	// wake says this attempt left room behind it. Without it the queue moves
	// one poll at a time: a release wakes the waiters, exactly one of them
	// takes a slot, and the second free slot sits there until somebody's timer
	// happens to fire — a cluster limit of eight that behaves like a limit of
	// one under load. Nothing notifies on a grant unless a grant is what made
	// room reachable for somebody else.
	wake bool
}

// attempt asks once, under the endpoint's lock, and either takes a slot or
// leaves the caller holding a queue position.
func (p *Postgres) attempt(ctx context.Context, model string, limit int, ticket *int64) (attemptResult, error) {
	var res attemptResult
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlLock, advisoryClass, model); err != nil {
			return err
		}
		// Dead nodes' slots and abandoned queue positions come back here, which
		// is why nothing else has to run a reaper: the only moment the numbers
		// matter is the moment somebody asks for them.
		if _, err := tx.Exec(ctx, sqlReap, model); err != nil {
			return err
		}
		if *ticket != 0 {
			// Still waiting, so still here: the refresh is what distinguishes a
			// queue position from a tombstone.
			var seq int64
			err := tx.QueryRow(ctx, sqlRefreshTicket, *ticket, p.cfg.TicketTTL.Seconds()).Scan(&seq)
			if errors.Is(err, pgx.ErrNoRows) {
				// Reaped while we were away. The place is lost — see the FIFO
				// note on Postgres — and a new one is taken below.
				*ticket = 0
			} else if err != nil {
				return err
			}
		}

		mine := noTicket
		if *ticket != 0 {
			mine = *ticket
		}
		var backoffMS int64
		var inUse, ahead int
		if err := tx.QueryRow(ctx, sqlTally, model, mine).Scan(&backoffMS, &inUse, &ahead); err != nil {
			return err
		}
		res.backoff = time.Duration(backoffMS) * time.Millisecond

		if res.backoff <= 0 && inUse+ahead < limit {
			id, err := slotID()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, sqlGrant, id, model, p.cfg.Node, ticketOrZero(*ticket), p.cfg.TTL.Seconds()); err != nil {
				return err
			}
			*ticket = 0
			res.granted, res.leaseID = true, id
			res.wake = inUse+1 < limit && ahead > 0
			return nil
		}
		if *ticket == 0 {
			// Not served, so join the queue — including when the endpoint is in
			// backoff, so that waiting out a 429 does not cost a place.
			if err := tx.QueryRow(ctx, sqlTakeTicket, model, p.cfg.Node, p.cfg.TicketTTL.Seconds()).Scan(ticket); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return attemptResult{}, err
	}
	return res, nil
}

// slotID names one held slot. It is random rather than derived from the node
// name because a node restarting must not be able to mint the id of a slot it
// held before the crash and heartbeat somebody else's.
func slotID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ticketOrZero maps "no ticket" onto a sequence number no row has, so sqlGrant
// can delete unconditionally rather than being two statements.
func ticketOrZero(ticket int64) int64 {
	if ticket == 0 {
		return -1
	}
	return ticket
}

func (p *Postgres) notify(ctx context.Context, model string) {
	_, _ = p.pool.Exec(ctx, sqlNotify, p.cfg.Channel, model)
}

// penalise closes the endpoint for every node after a rate limit.
//
// The rule is the local one — a report arriving while the endpoint is already
// closed is folded into the round in progress rather than escalating it —
// applied against the server's clock instead of this node's. That is the
// difference that matters in a cluster: twenty nodes all seeing the same
// refusal must produce one round of backoff, and they can only agree on that
// if they are all reading the same "is it still closed?".
func (p *Postgres) penalise(ctx context.Context, model string, retryAfter time.Duration) {
	policy := p.cfg.Backoff.normalised()
	_ = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlEnsureEndpoint, model); err != nil {
			return err
		}
		var attempts int
		var closed bool
		if err := tx.QueryRow(ctx, sqlLockEndpoint, model).Scan(&attempts, &closed); err != nil {
			return err
		}
		if closed {
			return nil
		}
		attempts++
		wait := policy.Delay(attempts, p.cfg.Rand)
		if retryAfter > wait {
			// The endpoint knows its own window; our schedule is a guess.
			wait = retryAfter
		}
		if wait > policy.Max {
			wait = policy.Max
		}
		_, err := tx.Exec(ctx, sqlCloseEndpoint, model, attempts, wait.Seconds())
		return err
	})
}

// recovered records that the endpoint served a call. A success while a backoff
// is still running proves nothing — it is a call that was already in flight —
// so the WHERE clause, evaluated on the server, is what makes it count.
func (p *Postgres) recovered(ctx context.Context, model string) {
	_, _ = p.pool.Exec(ctx, sqlRecovered, model)
}

// pgLease is one slot in the shared store.
type pgLease struct {
	p     *Postgres
	id    string
	model string
	done  atomic.Bool
}

var _ Lease = (*pgLease)(nil)

func (l *pgLease) TTL() time.Duration { return l.p.cfg.TTL }

// Heartbeat renews the slot. The expiry moves to now() + TTL on the server, so
// a node whose clock is an hour fast does not accidentally grant itself an hour
// of extra life.
func (l *pgLease) Heartbeat(ctx context.Context) error {
	if l.done.Load() {
		return ErrLeaseExpired
	}
	tag, err := l.p.pool.Exec(ctx, sqlHeartbeat, l.id, l.p.cfg.TTL.Seconds())
	if err != nil {
		// A store that cannot be reached is not a slot that was taken. Saying
		// otherwise here would cancel every call in flight the moment the
		// database hiccuped.
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseExpired
	}
	return nil
}

func (l *pgLease) Release(err error) {
	if !l.done.CompareAndSwap(false, true) {
		return // a second Release is a no-op, never a second slot.
	}
	// WithoutCancel and a deadline of its own: Release runs from a defer on the
	// way out of a model call, and the context that call used is usually
	// already dead. A slot that could only be returned by a healthy context
	// would leak on exactly the paths that matter.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), releaseTimeout)
	defer cancel()

	// The outcome is recorded before the slot moves, so the next node to be
	// granted it already sees the backoff this call just caused.
	switch retryAfter, limited := IsRateLimit(err); {
	case limited:
		l.p.penalise(ctx, l.model, retryAfter)
	case err == nil:
		l.p.recovered(ctx, l.model)
	}
	_, _ = l.p.pool.Exec(ctx, sqlFree, l.id, l.p.cfg.Channel)
}
