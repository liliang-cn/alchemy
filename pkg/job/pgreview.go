package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Hold stops a job for a person.
//
// It takes the reason rather than deriving it, because the reason is the whole
// of §7.3's first mechanic: a job merely offered for optional review and a job
// blocked on a conflict are the same state with two different lifetimes, and a
// store that guessed between them would either throw away an unanswered
// question over a weekend or hoard reviews nobody wanted.
//
// The lease ends here — node and deadline are cleared in the same statement
// that sets the state. Nobody is working a held job; a person is, and keeping
// a node responsible for it would lose the job when that node restarts, which
// is the failure the lease existed to prevent.
func (p *PG) Hold(ctx context.Context, l Lease, why HoldReason) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if why != HoldReview && why != HoldConflict {
		return &TransitionError{alchemy.JobRunning, alchemy.JobNeedsReview, actorWorker,
			"a hold must name a reason, because the reason picks the expiry"}
	}
	if err := check(alchemy.JobRunning, alchemy.JobNeedsReview, actorWorker); err != nil {
		return err
	}
	// The chosen TTL is bound as a parameter rather than selected in SQL by
	// the hold column. Both would work; this way the two durations live in one
	// place — Config — and a reader does not have to check whether the SQL and
	// holdTTL agree about which reason is which.
	const sql = `UPDATE {s}.jobs SET
	state = $6, node = '', deadline = NULL, hold = $7,
	expires_at = ` + nowSQL + ` + ($8::bigint * interval '1 microsecond')` + leaseWhere
	return p.write(ctx, l, p.q(sql),
		append(p.leaseArgs(l), string(alchemy.JobNeedsReview), int16(why), micros(p.holdTTL(why)))...)
}

// holdTTL is §7.3's second mechanic: optional review work can expire cheaply,
// a job blocked on a real question should outlive a long weekend, and neither
// of those is the other's timer.
func (p *PG) holdTTL(r HoldReason) time.Duration {
	if r == HoldConflict {
		return p.cfg.ConflictTTL
	}
	return p.cfg.ReviewTTL
}

// Resolve is a person answering the question the job asked.
//
// It takes no lease because the reviewer is not a node, and it is refused for
// any job that is not held: a caller reaching in to declare queued work
// SUCCEEDED is a bug, not a decision, and the message says which call they
// wanted instead.
func (p *PG) Resolve(ctx context.Context, id string, to alchemy.JobState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The table is consulted in the WHERE and not before it. Checking the
	// transition first would answer a question about a job that does not
	// exist: a reviewer who mistypes an ID would be told the move was illegal
	// and go looking for a job that was never there. "No such job" outranks
	// "no such transition", so the existence of the row has to be discovered
	// first — which the write itself does.
	const sql = `UPDATE {s}.jobs SET
	state = $2, node = '', deadline = NULL, hold = 0,
	expires_at = ` + nowSQL + ` + ($3::bigint * interval '1 microsecond')
WHERE id = $4 AND state = $5 AND state = ANY($6::text[])`
	tag, err := p.pool.Exec(ctx, p.q(sql),
		p.now(), string(to), micros(p.cfg.DoneTTL), id,
		string(alchemy.JobNeedsReview), froms(to, actorCaller))
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return p.notHeld(ctx, id, to)
	}
	return nil
}

// notHeld explains a resolve that did not land, in the order the reviewer
// needs to hear it: the job is missing, or it asked no question, or the answer
// was one a reviewer may not give.
func (p *PG) notHeld(ctx context.Context, id string, to alchemy.JobState) error {
	var state alchemy.JobState
	err := p.pool.QueryRow(ctx, p.q(`SELECT state FROM {s}.jobs WHERE id = $1`), id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("job: postgres: %w", err)
	}
	if state != alchemy.JobNeedsReview {
		return &TransitionError{state, to, actorCaller,
			"only a job held for a person can be resolved; to withdraw work use Cancel"}
	}
	// The job was held, so what the WHERE refused was the answer itself.
	if err := check(state, to, actorCaller); err != nil {
		return err
	}
	return &TransitionError{state, to, actorCaller,
		"the job changed state while the write was in flight"}
}
