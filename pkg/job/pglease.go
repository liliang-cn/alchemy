package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// leaseCols is a job plus the two numbers a Lease needs. deadline is nullable
// in the table — a job nobody holds has no deadline, and NULL says that
// better than the zero timestamp, which is a real instant in the year 1.
const leaseCols = jobCols + ", token, deadline"

func scanLease(row pgx.Row, node string) (Lease, error) {
	var j alchemy.Job
	var token int64
	var deadline *time.Time
	err := row.Scan(&j.ID, &j.State, &j.CreatedAt, &j.ExpiresAt, &j.Stage, &j.Error, &token, &deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, fmt.Errorf("job: postgres: %w", err)
	}
	j.CreatedAt, j.ExpiresAt = j.CreatedAt.UTC(), j.ExpiresAt.UTC()
	l := Lease{Job: j, Node: node, token: uint64(token)}
	if deadline != nil {
		l.Deadline = deadline.UTC()
	}
	return l, nil
}

// Claim takes the oldest claimable job.
//
// What this guarantees, said plainly, because SKIP LOCKED is not free:
//
//   - Uncontended, it is strict oldest-first — the in-memory store's promise,
//     unchanged, and the conformance suite holds it to that.
//   - Under contention, N nodes claiming at the same instant take the N oldest
//     jobs, but which node gets which is not ordered: a claimer skips rows
//     another transaction has locked rather than waiting behind them. So the
//     set is FIFO and the assignment is not.
//
// That is the trade and it is the right way round. The alternative — dropping
// SKIP LOCKED — makes every claimer queue behind the first, which converts a
// cluster's claim path into a serial one and makes a slow claim a cluster-wide
// stall. And the property that matters is the one that survives: no job is
// starved, because a job can only be passed over while N-1 older jobs are
// being claimed right now, never because it is unlucky.
//
// Ordering is by seq, an arrival counter, and not by created_at. Two jobs can
// share a timestamp — certainly under an injected clock, and easily under a
// real one — and a queue ordered by a column with ties is a queue whose order
// is the planner's opinion.
func (p *PG) Claim(ctx context.Context, node string, ttl time.Duration) (Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, false, err
	}
	if ttl <= 0 {
		return Lease{}, false, ErrLeaseTooShort
	}
	// nextval, not a column default and not `token = token + 1`. The fence is
	// store-wide because a job ID outlives the job: a client that retries under
	// the same name after the first job was collected gets a second, unrelated
	// job, and a counter that restarted per row would hand the node still
	// holding the first lease a token that is valid for the second.
	const sql = `UPDATE {s}.jobs SET
	state = $2,
	node = $3,
	token = nextval('{s}.job_fence'),
	lease_us = $4,
	deadline = ` + nowSQL + ` + ($4::bigint * interval '1 microsecond'),
	expires_at = ` + nowSQL + ` + ($4::bigint * interval '1 microsecond')
WHERE id = (
	SELECT id FROM {s}.jobs
	WHERE state = ANY($5::text[])
	  AND (state <> $2 OR deadline <= ` + nowSQL + `)
	ORDER BY seq
	LIMIT 1
	FOR UPDATE SKIP LOCKED
)
RETURNING ` + leaseCols
	l, err := scanLease(p.pool.QueryRow(ctx, p.q(sql),
		p.now(), string(alchemy.JobRunning), node, micros(ttl), froms(alchemy.JobRunning, actorWorker)), node)
	if errors.Is(err, ErrNotFound) {
		// An empty queue is (Lease{}, false, nil) rather than an error: a
		// worker loop polling an idle store is the normal case, and a store
		// that called it a failure would teach every operator to ignore the
		// failure.
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, err
	}
	return l, true, nil
}

// leaseWhere is the ownership test, moved out of a mutex and into a predicate.
// It is the whole of Mem.held: state, holder and token, checked in the same
// statement that writes, so there is no window between deciding and doing.
//
// The lease deadline is deliberately absent, exactly as in Mem: a node whose
// lease aged out while nobody wanted the job is still the node doing the work.
// What decides ownership is the token — if another node took the job, the
// number moved, and this node's writes stop landing at that instant.
const leaseWhere = ` WHERE id = $2 AND state = $3 AND node = $4 AND token = $5`

// leaseOwn is the same predicate with no clock parameter in front of it. A
// statement that binds $1 must mention it — an unreferenced parameter has no
// type the server can infer — so the one write that needs no clock gets its
// own numbering rather than a decorative coalesce to keep the shape uniform.
const leaseOwn = ` WHERE id = $1 AND state = $2 AND node = $3 AND token = $4`

func (p *PG) leaseArgs(l Lease) []any {
	return []any{p.now(), l.Job.ID, string(alchemy.JobRunning), l.Node, int64(l.token)}
}

// leaseRefused turns zero updated rows back into the sentence Mem would have
// produced, and it is the reason this store does not simply return
// ErrLeaseLost. "Was I refused" is never the interesting question; "was I
// overtaken, or was the job cancelled underneath me, or is it gone entirely"
// is, and those are three different things for the operator to do next.
func (p *PG) leaseRefused(ctx context.Context, l Lease) error {
	var state alchemy.JobState
	var holder string
	err := p.pool.QueryRow(ctx, p.q(`SELECT state, node FROM {s}.jobs WHERE id = $1`), l.Job.ID).Scan(&state, &holder)
	if errors.Is(err, pgx.ErrNoRows) {
		// Reaped, or deleted by its caller. Wrapped rather than replaced so
		// that errors.Is(err, ErrNotFound) still holds for the caller who only
		// wanted to know that much.
		return fmt.Errorf("job %s: %q wrote to a job the store no longer holds; it was reaped or deleted: %w",
			l.Job.ID, l.Node, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	return &LeaseError{JobID: l.Job.ID, Node: l.Node, Holder: holder, State: state}
}

// Heartbeat renews a lease and reports where the work has got to.
//
// The two are one call because they are one fact: a node that can say which
// stage it is in is a node that is alive, and a separate progress call would
// be a second thing to forget to make.
func (p *PG) Heartbeat(ctx context.Context, l Lease, stage string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	// Renewed by lease_us — the TTL the node asked for at Claim — and not by a
	// number this store picked: the node knows how long its chunks take. An
	// empty stage means "no news", not "no stage", because the heartbeat loop
	// and the code that knows about stages are rarely the same goroutine.
	const sql = `UPDATE {s}.jobs SET
	stage = CASE WHEN $6 = '' THEN stage ELSE $6 END,
	deadline = ` + nowSQL + ` + (lease_us * interval '1 microsecond'),
	expires_at = ` + nowSQL + ` + (lease_us * interval '1 microsecond')` +
		leaseWhere + ` RETURNING ` + leaseCols
	got, err := scanLease(p.pool.QueryRow(ctx, p.q(sql), append(p.leaseArgs(l), stage)...), l.Node)
	if errors.Is(err, ErrNotFound) {
		return Lease{}, p.leaseRefused(ctx, l)
	}
	if err != nil {
		return Lease{}, err
	}
	return got, nil
}

// Transition moves a job forward under a lease.
//
// It refuses the two states that need more than a name. A NEEDS_REVIEW without
// a reason cannot pick between §7.3's two timers, and a FAILED without a cause
// is a job nobody can debug; both are mistakes that would only be found in
// production, so they are refused here rather than defaulted.
func (p *PG) Transition(ctx context.Context, l Lease, to alchemy.JobState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch to {
	case alchemy.JobNeedsReview:
		return &TransitionError{alchemy.JobRunning, to, actorWorker,
			"a hold must say why, because the reason picks the expiry: use Hold"}
	case alchemy.JobFailed:
		return &TransitionError{alchemy.JobRunning, to, actorWorker,
			"a failure must say what went wrong: use Fail"}
	}
	// The from-state is pinned to RUNNING by leaseWhere, so the table can be
	// consulted here rather than in SQL: check is a pure function of a triple
	// this store already knows. Nothing about the row can make an illegal move
	// legal, which is why asking first costs no correctness.
	if err := check(alchemy.JobRunning, to, actorWorker); err != nil {
		return err
	}
	switch to {
	case alchemy.JobPending:
		return p.requeue(ctx, l)
	case alchemy.JobRunning:
		// The table's one self-transition, and it must not be treated as "some
		// terminal state we have not named": the holder is unchanged, so the
		// only thing to write is the job's expiry, which for a running job is
		// its lease deadline and nothing else. Falling through to finish()
		// here would clear node and deadline and hand the caller's own live
		// lease back to the queue.
		//
		// leaseOwn rather than leaseWhere: this is the only write whose new
		// expiry is already on the row, so it binds no clock at all.
		return p.write(ctx, l, p.q(`UPDATE {s}.jobs SET expires_at = deadline`+leaseOwn),
			l.Job.ID, string(alchemy.JobRunning), l.Node, int64(l.token))
	}
	return p.finish(ctx, l, to, "")
}

// Fail ends a job with the reason attached.
func (p *PG) Fail(ctx context.Context, l Lease, cause string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cause == "" {
		return &TransitionError{alchemy.JobRunning, alchemy.JobFailed, actorWorker,
			"a failure must say what went wrong"}
	}
	if err := check(alchemy.JobRunning, alchemy.JobFailed, actorWorker); err != nil {
		return err
	}
	return p.finish(ctx, l, alchemy.JobFailed, cause)
}

// Release hands unfinished work back to the queue — a node shutting down
// cleanly, or one that decided it is the wrong node for this job. It is the
// polite version of the lease simply dying.
func (p *PG) Release(ctx context.Context, l Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := check(alchemy.JobRunning, alchemy.JobPending, actorWorker); err != nil {
		return err
	}
	return p.requeue(ctx, l)
}

// requeue is the RUNNING -> PENDING write. The stage is cleared with the node:
// leaving "extract" on a job nobody is working is how a progress display lies.
func (p *PG) requeue(ctx context.Context, l Lease) error {
	const sql = `UPDATE {s}.jobs SET
	state = $6, node = '', deadline = NULL, hold = 0, stage = '',
	expires_at = ` + nowSQL + ` + ($7::bigint * interval '1 microsecond')` + leaseWhere
	return p.write(ctx, l, p.q(sql),
		append(p.leaseArgs(l), string(alchemy.JobPending), micros(p.cfg.PendingTTL))...)
}

// finish is the RUNNING -> terminal write. A finished job is kept only long
// enough to be collected: §5c, the print queue must not become a filesystem by
// the slow route either.
func (p *PG) finish(ctx context.Context, l Lease, to alchemy.JobState, cause string) error {
	const sql = `UPDATE {s}.jobs SET
	state = $6, node = '', deadline = NULL, hold = 0,
	error = CASE WHEN $7 = '' THEN error ELSE $7 END,
	expires_at = ` + nowSQL + ` + ($8::bigint * interval '1 microsecond')` + leaseWhere
	return p.write(ctx, l, p.q(sql),
		append(p.leaseArgs(l), string(to), cause, micros(p.cfg.DoneTTL))...)
}

// write executes a lease-guarded update and explains a refusal. Every write in
// this file goes through it, so "the predicate and the write are one
// statement" is a property of the code rather than a habit.
func (p *PG) write(ctx context.Context, l Lease, sql string, args ...any) error {
	tag, err := p.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return p.leaseRefused(ctx, l)
	}
	return nil
}
