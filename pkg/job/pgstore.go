package job

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// PG is the clustered Store of §8.3: the one real shared implementation, so
// that a node that dies mid-job does not take the job with it.
//
// It lives in package job rather than in a package of its own for the reason
// lease.go already gives: a Lease carries an unexported fence token, so only
// this package can mint one, and an exported constructor that let a second
// package mint a Lease would let a worker mint one too. The alternative — a
// build tag — would keep this file out of the default `go build` and `go vet`,
// and the file that talks to a database over a network is the last one that
// should be excluded from the checks everyone actually runs.
type PG struct {
	pool *pgxpool.Pool
	cfg  Config
	// schema qualifies every statement instead of relying on search_path. A
	// pool hands out connections that a session-level SET does not reliably
	// follow, and "the sweeper deleted rows in the wrong schema" is a bug
	// discovered exactly once.
	schema string
	// batch caps the rows one sweep statement touches. Without it a first
	// sweep of a backlogged table holds one transaction across every expired
	// row, which blocks the vacuum and the workers alike; see Expire.
	batch int
	// live is the set of non-terminal states, derived from the transition
	// table rather than written out again. Terminal is expressed as "not
	// live" everywhere it is needed, so a state added to the table with no
	// outgoing edges is reaped and counted correctly without anyone
	// remembering to update a second list.
	live []string
	// owns the pool: a store built from a caller's pool must not close it.
	owns bool
	// sweeps counts the statements Expire has issued, so a test can prove the
	// batching is real rather than assert that the results happen to be right.
	sweeps atomic.Int64
}

// PGConfig is Config plus the two knobs that only exist once the store is a
// table. They are here rather than in Config because a field that means
// nothing to the in-memory store is a field an operator has to be told to
// ignore.
type PGConfig struct {
	Config
	// Schema is where the tables live. Empty means "public". A schema per
	// deployment is how two Alchemy clusters share one database without one
	// sweeping the other's jobs.
	Schema string
	// SweepBatch is how many rows one Expire statement may touch. Expire loops
	// until a pass is short, so this bounds transaction length and not the
	// work done.
	SweepBatch int
}

const defaultSweepBatch = 1000

// identRE guards the one string in this package that is interpolated into SQL
// rather than bound as a parameter. An identifier cannot be a placeholder, so
// it is validated at construction instead of trusted at every call site.
var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// OpenPG dials the DSN and returns a store. Migrate still has to be called;
// they are separate because a node in a cluster of ten should be able to start
// without ten nodes racing to create the same table on every deploy.
func OpenPG(ctx context.Context, dsn string, cfg PGConfig) (*PG, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("job: postgres: %w", err)
	}
	p, err := NewPG(pool, cfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	p.owns = true
	return p, nil
}

// NewPG builds a store on a pool the caller already has, which is how a
// service that already talks to this database avoids a second pool.
func NewPG(pool *pgxpool.Pool, cfg PGConfig) (*PG, error) {
	if cfg.Schema == "" {
		cfg.Schema = "public"
	}
	if !identRE.MatchString(cfg.Schema) {
		return nil, fmt.Errorf("job: postgres: %q is not a usable schema name", cfg.Schema)
	}
	if cfg.SweepBatch <= 0 {
		cfg.SweepBatch = defaultSweepBatch
	}
	// The same defaults as Mem, and deliberately not the same code path: a
	// zero Config must mean the same thing in both stores, and the way to be
	// sure is that the conformance suite runs against both.
	c := cfg.Config
	if c.Capacity <= 0 {
		c.Capacity = defaultCapacity
	}
	if c.PendingTTL <= 0 {
		c.PendingTTL = defaultPendingTTL
	}
	if c.ReviewTTL <= 0 {
		c.ReviewTTL = defaultReviewTTL
	}
	if c.ConflictTTL <= 0 {
		c.ConflictTTL = defaultConflictTTL
	}
	if c.DoneTTL <= 0 {
		c.DoneTTL = defaultDoneTTL
	}
	// Clock is deliberately left nil. Mem substitutes the wall clock here; for
	// a cluster that would be the wrong default, because then every node's
	// idea of when a lease dies is its own — see now().
	p := &PG{pool: pool, cfg: c, schema: cfg.Schema, batch: cfg.SweepBatch}
	for from := range legal {
		p.live = append(p.live, string(from))
	}
	return p, nil
}

// Close releases the pool, if this store opened it.
func (p *PG) Close() {
	if p.owns {
		p.pool.Close()
	}
}

// now is the answer to the question this store exists to get right.
//
// A nil Clock — the production configuration — returns nil, and every
// statement below reads `coalesce($1::timestamptz, now())`. So in a cluster
// there is exactly one clock, the database's, and no node's skew can shorten
// another node's lease or expire a job early. The alternative, computing
// deadlines in Go and sending instants, means the answer to "is this lease
// dead" depends on which node asked, which is a distributed-systems bug that
// presents as "jobs are occasionally run twice on Tuesdays".
//
// A non-nil Clock is the test path, and it is the same statement with $1
// bound: expiry is proved by moving a ManualClock rather than by sleeping.
// That it is the *only* way node time enters the SQL is what makes the choice
// above a property of the code rather than a claim in a comment.
// A nil is bound as a NULL parameter and typed by the ::timestamptz cast in
// nowSQL, not by the Go value — which is why every statement that binds it
// must actually mention it. The one write that does not need a clock uses
// leaseOwn below and binds four parameters instead of five.
func (p *PG) now() any {
	if p.cfg.Clock == nil {
		return nil
	}
	return p.cfg.Clock.Now().UTC()
}

// nowSQL is the clock expression. $1 is the injected instant in every
// statement in this store, so there is one convention to remember.
const nowSQL = "coalesce($1::timestamptz, now())"

// micros renders a Go duration as the integer a SQL interval is built from.
// Postgres stores timestamps to the microsecond, so a TTL with nanoseconds in
// it would round on the way in and make a test's arithmetic fail by 1ns.
func micros(d time.Duration) int64 { return int64(d / time.Microsecond) }

// jobCols is the projection of the wire type. Everything else in the table —
// the lease holder, the token, the hold reason — is the store's business, and
// putting it in alchemy.Job would invite a client to reason about another
// node's lease.
const jobCols = "id, state, created_at, expires_at, stage, error"

func scanJob(row pgx.Row) (alchemy.Job, error) {
	var j alchemy.Job
	err := row.Scan(&j.ID, &j.State, &j.CreatedAt, &j.ExpiresAt, &j.Stage, &j.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return alchemy.Job{}, ErrNotFound
	}
	if err != nil {
		return alchemy.Job{}, fmt.Errorf("job: postgres: %w", err)
	}
	// Times come back in the session's zone. Every comparison in this package
	// is against an instant, but a caller that formats one should see UTC
	// rather than whatever zone the database happened to be configured with.
	j.CreatedAt, j.ExpiresAt = j.CreatedAt.UTC(), j.ExpiresAt.UTC()
	return j, nil
}

// q interpolates the schema. The only thing ever interpolated is p.schema,
// which identRE has already vetted.
func (p *PG) q(format string) string {
	return strings.ReplaceAll(format, "{s}", p.schema)
}

// Migrate creates the tables. It is idempotent and safe to run from every node
// at once: the advisory lock serialises the DDL, so ten nodes starting
// together produce one table rather than nine "already exists" crashes.
func (p *PG) Migrate(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "alchemy.job."+p.schema); err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	for _, stmt := range p.ddl() {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("job: postgres: %s: %w", firstLine(stmt), err)
		}
	}
	return tx.Commit(ctx)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (p *PG) ddl() []string {
	return []string{
		// One sequence for the whole store, never a column default and never a
		// per-job counter. See Claim: a job ID outlives the job, so a token
		// that restarts for each row is valid for a job it has never seen.
		p.q(`CREATE SEQUENCE IF NOT EXISTS {s}.job_fence AS bigint START 1`),
		p.q(`CREATE TABLE IF NOT EXISTS {s}.jobs (
	id          text PRIMARY KEY,
	-- seq is arrival order, and it is not created_at. Under an injected clock
	-- every job in a test is created at the same instant, and in production two
	-- jobs can share a millisecond; ordering a queue by a column with ties
	-- turns FIFO into whatever the planner felt like.
	seq         bigserial NOT NULL,
	state       text NOT NULL,
	created_at  timestamptz NOT NULL,
	expires_at  timestamptz NOT NULL,
	stage       text NOT NULL DEFAULT '',
	error       text NOT NULL DEFAULT '',
	-- The lease. node and token are the store's evidence of ownership; ttl is
	-- remembered so a heartbeat renews by the number the node asked for.
	node        text NOT NULL DEFAULT '',
	token       bigint NOT NULL DEFAULT 0,
	lease_us    bigint NOT NULL DEFAULT 0,
	deadline    timestamptz,
	hold        smallint NOT NULL DEFAULT 0
)`),
		// Claim reads this one: partial, so it is the size of the queue rather
		// than the size of the table, and ordered so the oldest is the first
		// row rather than a sort of everything claimable.
		p.q(`CREATE INDEX IF NOT EXISTS jobs_claimable ON {s}.jobs (seq)
	WHERE state IN ('PENDING', 'RUNNING')`),
		// Admission reads this one. It is what makes count(*) affordable: the
		// index holds only live rows, and live rows are bounded by Capacity —
		// which is the number the count is checking against.
		p.q(`CREATE INDEX IF NOT EXISTS jobs_live ON {s}.jobs (state)
	WHERE state IN ('PENDING', 'RUNNING', 'NEEDS_REVIEW')`),
		// The sweeper reads this one: three predicates, all on expires_at.
		p.q(`CREATE INDEX IF NOT EXISTS jobs_expiry ON {s}.jobs (expires_at)`),
	}
}

// Create admits a job.
//
// One statement, and it has to be one statement: two retrying clients race
// here by construction (§8.3's at-least-once applied one step earlier than the
// writes it was written about), and a SELECT-then-INSERT would admit the same
// work twice or refuse a retry that had already been admitted.
//
// The capacity predicate is inside the INSERT's WHERE rather than before it,
// which is what makes the two requirements compose: a full store still answers
// a retry, because a row excluded by the capacity predicate produces the same
// zero rows as a conflict, and the follow-up SELECT tells them apart by
// looking for the job. Refusing a retry because the queue is full would turn a
// client's duplicate into a failure at the moment the operator can least
// afford one.
func (p *PG) Create(ctx context.Context, id string) (alchemy.Job, error) {
	if err := ctx.Err(); err != nil {
		return alchemy.Job{}, err
	}
	if id == "" {
		id = mintID()
	}
	const sql = `INSERT INTO {s}.jobs (id, state, created_at, expires_at)
SELECT $2, $3, ` + nowSQL + `, ` + nowSQL + ` + ($4::bigint * interval '1 microsecond')
WHERE (SELECT count(*) FROM {s}.jobs WHERE state = ANY($5::text[])) < $6::int
ON CONFLICT (id) DO NOTHING
RETURNING ` + jobCols
	j, err := scanJob(p.pool.QueryRow(ctx, p.q(sql),
		p.now(), id, string(alchemy.JobPending), micros(p.cfg.PendingTTL), p.live, p.cfg.Capacity))
	if err == nil {
		return j, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return alchemy.Job{}, err
	}

	// Nothing was inserted: either the ID is taken, or the queue is full. The
	// stored job answers both questions, and it is read *without* touching the
	// deadlines — refreshing a retry's expiry would let a client that retries
	// every minute hold a job open forever, and the expiry §5c insists on
	// would never fire.
	existing, err := p.Get(ctx, id)
	if err == nil {
		return existing, ErrExists
	}
	if !errors.Is(err, ErrNotFound) {
		return alchemy.Job{}, err
	}
	// Nothing inserted and no row to find: the capacity predicate refused it.
	// Saying so with the numbers attached is the difference between an
	// operator raising a limit and an operator restarting a node that was
	// never stuck.
	return alchemy.Job{}, p.capacityError(ctx)
}

// capacityError attaches the numbers to the refusal. The count is a second
// statement and can therefore disagree with the one that refused; it is a
// diagnostic, and a refusal that already happened is not undone by a number
// that moved.
func (p *PG) capacityError(ctx context.Context) error {
	var n int
	err := p.pool.QueryRow(ctx, p.q(`SELECT count(*) FROM {s}.jobs WHERE state = ANY($1::text[])`), p.live).Scan(&n)
	if err != nil {
		return &CapacityError{Capacity: p.cfg.Capacity, Live: p.cfg.Capacity}
	}
	return &CapacityError{Capacity: p.cfg.Capacity, Live: n}
}

func (p *PG) Get(ctx context.Context, id string) (alchemy.Job, error) {
	if err := ctx.Err(); err != nil {
		return alchemy.Job{}, err
	}
	return scanJob(p.pool.QueryRow(ctx, p.q(`SELECT `+jobCols+` FROM {s}.jobs WHERE id = $1`), id))
}

func (p *PG) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, p.q(`DELETE FROM {s}.jobs WHERE id = $1`), id)
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// froms is the set of states a given actor may reach `to` from, read out of
// the transition table. It is what moves the check into the WHERE clause
// without copying the table into SQL: the predicate is generated from the same
// map that check() reads, so the two cannot drift.
func froms(to alchemy.JobState, by actor) []string {
	var out []string
	for from, edges := range legal {
		if edges[to]&by != 0 {
			out = append(out, string(from))
		}
	}
	return out
}

// Cancel withdraws a job on behalf of the caller.
//
// The check and the write are one statement — `AND state = ANY(...)` is the
// transition table, and zero rows is the refusal. What zero rows does not
// carry is the *reason*, so refused() reads the row back to build the message
// an operator can act on.
func (p *PG) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	const sql = `UPDATE {s}.jobs SET
	state = $2, node = '', deadline = NULL, hold = 0,
	expires_at = ` + nowSQL + ` + ($3::bigint * interval '1 microsecond')
WHERE id = $4 AND state = ANY($5::text[])`
	tag, err := p.pool.Exec(ctx, p.q(sql),
		p.now(), string(alchemy.JobCancelled), micros(p.cfg.DoneTTL), id, froms(alchemy.JobCancelled, actorCaller))
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return p.refused(ctx, id, alchemy.JobCancelled, actorCaller)
	}
	return nil
}

// refused turns "0 rows updated" back into the sentence the in-memory store
// would have produced under its mutex.
//
// It is a second read, and it can race: between the refused write and this
// SELECT the job may have moved again. That is acceptable and the reason is
// worth stating — the race can only change the wording of a refusal that has
// already happened, never whether it happened. The write did not land; this
// only decides what to call it.
func (p *PG) refused(ctx context.Context, id string, to alchemy.JobState, by actor) error {
	var state alchemy.JobState
	err := p.pool.QueryRow(ctx, p.q(`SELECT state FROM {s}.jobs WHERE id = $1`), id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	if err := check(state, to, by); err != nil {
		return err
	}
	// The state now permits the move that was just refused, so the row changed
	// underneath us. Reporting success would be a lie — nothing was written.
	return &TransitionError{state, to, by, "the job changed state while the write was in flight"}
}
