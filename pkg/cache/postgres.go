package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the shared content-addressed cache of §8.2: one row per address,
// visible to every node, so a job resumed on a different machine after a lease
// expiry (§8.3) re-buys the chunks that had not finished rather than all of
// them.
//
// Three decisions are worth reading before the code.
//
// **The wire format is alchemy's own JSON, over a declared attribute domain.**
// The entities and relations are stored as the same JSON §4 returns, encoded by
// the same struct tags, so a cached entry and a fresh one cannot drift apart
// when somebody edits a tag. What makes that safe is ErrUnsupportedAttribute:
// the attribute value domain is JSON's, checked before anything is written, so
// nothing can be stored that would come back a different Go type. Read the note
// on ErrUnsupportedAttribute for why that is a codification rather than a
// restriction.
//
// **Eviction is by age, not by recency, and that is a deliberate downgrade from
// the in-process cache's LRU.** LRU needs a write per read — Get would have to
// stamp a recency column — and across a cluster that turns every cache hit into
// a row update, a WAL record and eventually a vacuum, on the one path that has
// to stay cheap. The access pattern §8.2 describes does not need it: a resumed
// job re-reads chunks that were written by the run it is resuming, so age since
// write and time since last read are nearly the same predictor here, and only
// one of them is free. So Get is a plain SELECT that touches nothing, and what
// falls out of the cache is what has been there longest. The in-process cache
// keeps LRU because there a read is a pointer move.
//
// **Two nodes that miss the same address at the same moment both buy the
// call.** Neither the correctness of the result nor the address it is stored
// under is affected — that is what content addressing is for — but the saving
// is lost for that one chunk. It is not fixed here, and not because it is hard.
// The fix is a reservation row that says "somebody is computing this", and a
// reservation held across a model call is a lock held for as long as the call
// takes: a node that dies mid-extraction then blocks every other node from
// that chunk until the reservation expires, and a reservation short enough not
// to do that is short enough to expire during a slow call and let the second
// node in anyway. Paying twice for one chunk in a rare window is the cheaper
// failure, and §7.2 already says cost is not the constraint quality is.
type Postgres struct {
	pool *pgxpool.Pool
	cfg  PostgresConfig

	// lastSweep is the unix nano of the last opportunistic sweep. It is here so
	// a cache with a bound sweeps itself: an operator who has to remember to
	// run a cron job is an operator whose cache table is the largest thing in
	// the database by the time anybody notices.
	lastSweep atomic.Int64
}

var _ Cache = (*Postgres)(nil)

// PostgresConfig bounds the shared cache. The zero value is a cache that keeps
// everything forever, which is the right default for a store whose entries are
// small and whose misses cost model calls — but it is a decision an operator
// should be able to change without reading the code, hence the two knobs.
type PostgresConfig struct {
	// MaxAge is how long an entry stays valid. Zero keeps entries forever.
	//
	// It is enforced on read as well as by Sweep, so what the cache returns
	// never depends on when a sweep last ran. A cache whose answers change
	// according to the maintenance schedule is one that cannot be reasoned
	// about from a job's output.
	MaxAge time.Duration
	// MaxEntries caps the table, oldest first. Zero is unbounded.
	MaxEntries int
	// SweepEvery bounds how often a Put may pay for a sweep. Default one
	// minute, and it only applies when MaxAge or MaxEntries is set.
	SweepEvery time.Duration
}

const defaultSweepEvery = time.Minute

const cacheSchemaSQL = `
CREATE TABLE IF NOT EXISTS alchemy_cache_entry (
	address   text PRIMARY KEY,
	entities  jsonb NOT NULL,
	relations jsonb NOT NULL,
	tokens    integer NOT NULL,
	stored_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS alchemy_cache_entry_stored_at_idx ON alchemy_cache_entry (stored_at);
`

// cacheAdvisoryClass namespaces this package's advisory lock away from any
// other component's.
const cacheAdvisoryClass = int32(0x616C6363)

const (
	sqlCacheExists = `SELECT to_regclass('alchemy_cache_entry') IS NOT NULL`
	sqlCacheLock   = `SELECT pg_advisory_xact_lock($1, hashtext($2))`

	// sqlCacheGet applies MaxAge in the WHERE clause, on the server's clock:
	// two nodes with skewed clocks must not disagree about whether an entry is
	// still good.
	sqlCacheGet = `
SELECT entities, relations, tokens
FROM alchemy_cache_entry
WHERE address = $1
  AND ($2::float8 <= 0 OR stored_at > now() - make_interval(secs => $2))`

	// sqlCachePut overwrites rather than doing nothing on conflict. The same
	// address is the same answer to the same question, so the two writers of
	// §8.3's brief overlap cannot disagree; refreshing stored_at is what keeps
	// an entry a live job is still writing from ageing out underneath it.
	sqlCachePut = `
INSERT INTO alchemy_cache_entry (address, entities, relations, tokens, stored_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (address) DO UPDATE
SET entities = EXCLUDED.entities, relations = EXCLUDED.relations,
    tokens = EXCLUDED.tokens, stored_at = now()`

	sqlCacheSweepAge = `DELETE FROM alchemy_cache_entry WHERE stored_at <= now() - make_interval(secs => $1)`
	sqlCacheSweepCap = `
DELETE FROM alchemy_cache_entry WHERE address IN (
	SELECT address FROM alchemy_cache_entry ORDER BY stored_at DESC, address DESC OFFSET $1
)`
)

// NewPostgres creates the table if it is missing and returns the shared cache.
//
// The existence check before the DDL is not an optimisation. It means a node
// pointed at a read-only connection — a replica, or a deployment that does its
// migrations elsewhere — can still serve hits, and a cache that could only be
// opened by something allowed to alter the schema would be one no read replica
// could ever help with.
func NewPostgres(ctx context.Context, pool *pgxpool.Pool, cfg PostgresConfig) (*Postgres, error) {
	if pool == nil {
		return nil, errors.New("cache: NewPostgres needs a pool")
	}
	if cfg.MaxEntries < 0 {
		return nil, errors.New("cache: PostgresConfig.MaxEntries cannot be negative")
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = defaultSweepEvery
	}
	if err := cacheMigrate(ctx, pool); err != nil {
		return nil, err
	}
	return &Postgres{pool: pool, cfg: cfg}, nil
}

func cacheMigrate(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx, sqlCacheExists).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	// Under a lock because two nodes starting together both run CREATE TABLE IF
	// NOT EXISTS, and Postgres answers that race with "tuple concurrently
	// updated" rather than with a table.
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlCacheLock, cacheAdvisoryClass, "alchemy_cache_schema"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, cacheSchemaSQL)
		return err
	})
}

// Get returns the entry stored at k's address. A miss is (Entry{}, false, nil);
// an error means the store itself failed, and Fetch's contract says a caller
// treats that as a miss and buys the call.
func (p *Postgres) Get(ctx context.Context, k Key) (Entry, bool, error) {
	var e Entry
	err := p.pool.QueryRow(ctx, sqlCacheGet, k.Address(), p.cfg.MaxAge.Seconds()).
		Scan(&e.Entities, &e.Relations, &e.Tokens)
	if errors.Is(err, pgx.ErrNoRows) {
		// A miss is not an error: the work simply has not been done yet.
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// Put stores the entry. Nothing is written until every attribute has been
// checked, so an entry the cache cannot return unchanged is never half stored.
func (p *Postgres) Put(ctx context.Context, k Key, e Entry) error {
	if err := validate(e); err != nil {
		return err
	}
	// The slices are handed to pgx as they are: it encodes them with
	// encoding/json, which is the same encoder §4's result goes through. A nil
	// slice becomes JSON null and reads back as a nil slice, so an entry that
	// stated no relations comes back stating none rather than stating an empty
	// list — the two serialise differently and the JSON is the contract (§4).
	if _, err := p.pool.Exec(ctx, sqlCachePut, k.Address(), e.Entities, e.Relations, e.Tokens); err != nil {
		return err
	}
	p.maybeSweep(ctx)
	return nil
}

// Sweep removes what the configuration says is no longer worth keeping and
// reports how many rows went. It is exported because an operator with a cache
// table they want smaller now should not have to wait for a Put.
func (p *Postgres) Sweep(ctx context.Context) (int64, error) {
	var removed int64
	if p.cfg.MaxAge > 0 {
		tag, err := p.pool.Exec(ctx, sqlCacheSweepAge, p.cfg.MaxAge.Seconds())
		if err != nil {
			return removed, err
		}
		removed += tag.RowsAffected()
	}
	if p.cfg.MaxEntries > 0 {
		tag, err := p.pool.Exec(ctx, sqlCacheSweepCap, p.cfg.MaxEntries)
		if err != nil {
			return removed, err
		}
		removed += tag.RowsAffected()
	}
	p.lastSweep.Store(time.Now().UnixNano())
	return removed, nil
}

// maybeSweep runs a sweep at most once per SweepEvery, and never at all for an
// unbounded cache. Its error is dropped on purpose: a sweep that failed has not
// invalidated the entry that was just stored, and a Put that failed because
// housekeeping failed would make the cache able to break a job — the one thing
// the Cache contract says it must not do.
func (p *Postgres) maybeSweep(ctx context.Context) {
	if p.cfg.MaxAge <= 0 && p.cfg.MaxEntries <= 0 {
		return
	}
	now := time.Now().UnixNano()
	last := p.lastSweep.Load()
	if now-last < int64(p.cfg.SweepEvery) {
		return
	}
	if !p.lastSweep.CompareAndSwap(last, now) {
		return // another goroutine is already paying for it.
	}
	_, _ = p.Sweep(ctx)
}
