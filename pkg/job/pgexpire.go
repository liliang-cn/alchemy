package job

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

var _ Store = (*PG)(nil)

// Expire runs one pass of the sweeper.
//
// It takes no time argument, for the same reason Mem.Expire does not: the
// store already has a clock, and a method that accepted a second opinion about
// what time it is would let a caller expire work early by passing a future
// instant. In this store that argument is stronger still — the clock is the
// database's, so one pass means the same thing from every node.
//
// Three statements, one meaning each, and every one of them batched. A first
// sweep of a table that has been running for a month can match hundreds of
// thousands of rows, and a single unbounded UPDATE would hold one transaction
// across all of them: it blocks vacuum, it pins an xmin horizon, and it makes
// the sweeper's first run after an outage the outage. Looping small statements
// gives up nothing — a partial pass is a correct pass, because everything it
// did was a checked transition and the next pass finds the rest.
//
// Two sweepers running at once is also fine, and that is what SKIP LOCKED is
// doing here rather than in Claim: each row is reported by whichever sweeper
// locked it, and the other does not block waiting to report it again.
func (p *PG) Expire(ctx context.Context) (Swept, error) {
	if err := ctx.Err(); err != nil {
		return Swept{}, err
	}
	var out Swept
	// A node that died does not take the job with it (§8.3). This runs first
	// so that a requeued job gets a fresh PENDING timer before the expiry pass
	// looks at PENDING jobs — otherwise one pass could discard the work it had
	// just decided to retry.
	const requeue = `UPDATE {s}.jobs SET
	state = $2, node = '', deadline = NULL, hold = 0, stage = '',
	expires_at = ` + nowSQL + ` + ($3::bigint * interval '1 microsecond')
WHERE id IN (
	SELECT id FROM {s}.jobs
	WHERE state = $4 AND deadline <= ` + nowSQL + `
	ORDER BY seq LIMIT $5 FOR UPDATE SKIP LOCKED
)
RETURNING id`
	ids, err := p.sweep(ctx, p.q(requeue),
		p.now(), string(alchemy.JobPending), micros(p.cfg.PendingTTL), string(alchemy.JobRunning))
	if err != nil {
		return Swept{}, err
	}
	out.Requeued = ids

	// §5c's obligation: queued work nobody claimed and held work nobody
	// answered both age out, or the service quietly grows a database of
	// abandoned reviews. An expired job is terminal and gets the retention
	// timer, so the reap below will not see it this pass.
	const expire = `UPDATE {s}.jobs SET
	state = $2, node = '', deadline = NULL, hold = 0,
	expires_at = ` + nowSQL + ` + ($3::bigint * interval '1 microsecond')
WHERE id IN (
	SELECT id FROM {s}.jobs
	WHERE state = ANY($4::text[]) AND expires_at <= ` + nowSQL + `
	ORDER BY seq LIMIT $5 FOR UPDATE SKIP LOCKED
)
RETURNING id`
	ids, err = p.sweep(ctx, p.q(expire),
		p.now(), string(alchemy.JobExpired), micros(p.cfg.DoneTTL), froms(alchemy.JobExpired, actorSweeper))
	if err != nil {
		return Swept{}, err
	}
	out.Expired = ids

	// Finished work, collected or not. The predicate is "not live" rather than
	// a list of terminal states, so a state added to the transition table with
	// no outgoing edges is reaped without anyone remembering to add it here.
	const reap = `DELETE FROM {s}.jobs WHERE id IN (
	SELECT id FROM {s}.jobs
	WHERE NOT (state = ANY($2::text[])) AND expires_at <= ` + nowSQL + `
	ORDER BY seq LIMIT $3 FOR UPDATE SKIP LOCKED
)
RETURNING id`
	ids, err = p.sweep(ctx, p.q(reap), p.now(), p.live)
	if err != nil {
		return Swept{}, err
	}
	out.Reaped = ids
	return out, nil
}

// sweep runs one batched statement until a pass comes back short. The batch
// size is the last parameter of every statement above, appended here so that
// no caller can forget it — an unbatched sweep statement is the bug this
// method exists to make unwritable.
func (p *PG) sweep(ctx context.Context, sql string, args ...any) ([]string, error) {
	var out []string
	for {
		p.sweeps.Add(1)
		rows, err := p.pool.Query(ctx, sql, append(args, p.batch)...)
		if err != nil {
			return nil, fmt.Errorf("job: postgres: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("job: postgres: %w", err)
		}
		out = append(out, ids...)
		if len(ids) < p.batch {
			// A short pass means the predicate is satisfied, or that another
			// sweeper holds what is left. Either way there is nothing here for
			// this pass to do, and looping to find out again is how a sweeper
			// turns an idle cluster into a busy one.
			return out, nil
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
}
