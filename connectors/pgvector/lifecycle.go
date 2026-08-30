package pgvector

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// childTables are every table a load owns, in the order a delete has to walk
// them: children before the row they reference.
//
// A table missing from here is invisible until somebody deletes a load: the
// rows survive their load, the load row's own DELETE then fails on the foreign
// key, and the first symptom is a sweep that quietly stopped working. It is
// derived from nothing, so TestEveryTableALoadOwnsIsOnTheDeletePath derives it
// from the DDL and compares.
var childTables = []string{"chunks", "entities", "relations", "violations", "duplicates", "supersessions"}

// deleteLoad removes a load and everything it wrote, in batches.
//
// The batching is the same lesson pkg/job's sweeper learned and states: a
// single unbounded DELETE over a four-hundred-thousand-row load holds one
// transaction across all of it, which blocks vacuum, pins an xmin horizon, and
// makes the cleanup after an outage into the outage. Looping small statements
// gives up nothing, because a partial delete of an invisible load is still an
// invisible load and the next pass finds the rest.
//
// The foreign keys declare ON DELETE CASCADE as well. That is the safety net
// for a buyer who deletes a load row by hand, not the path taken here: a
// cascade would do the whole thing in the one unbounded transaction this
// method exists to avoid.
func (l *Loader) deleteLoad(ctx context.Context, id string) error {
	for _, table := range childTables {
		sql := l.q(fmt.Sprintf(
			`DELETE FROM {s}.%s WHERE ctid IN (SELECT ctid FROM {s}.%s WHERE load_id = $1 LIMIT $2)`,
			table, table))
		for {
			tag, err := l.pool.Exec(ctx, sql, id, l.batch)
			if err != nil {
				return fmt.Errorf("pgvector: deleting %s of load %s: %w", table, id, err)
			}
			if tag.RowsAffected() < int64(l.batch) {
				break
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	if _, err := l.pool.Exec(ctx, l.q(`DELETE FROM {s}.loads WHERE id = $1`), id); err != nil {
		return fmt.Errorf("pgvector: deleting load %s: %w", id, err)
	}
	return nil
}

// Delete removes one load by name. A buyer who re-imports a corpus and wants
// the old graph gone has to be able to say so, and saying it through the
// connector gets the batching rather than a cascade that locks the table.
func (l *Loader) Delete(ctx context.Context, id string) error {
	return l.deleteLoad(ctx, id)
}

// abandon is the best-effort cleanup after a load fails halfway.
//
// Two things about it are deliberate. It runs on a context detached from the
// caller's, because the commonest way a load fails halfway is that the
// caller's context was cancelled, and a cleanup that inherits the cancellation
// is a cleanup that never runs. And its error is dropped rather than returned,
// because the caller is already being handed the error that matters and
// replacing it with "and the cleanup also failed" would hide the cause.
//
// What happens when it does fail is the thing worth stating: nothing. The load
// row stays in `loading`, every read view excludes it, and Sweep removes it
// later. The store is never wrong, only temporarily larger — which is the
// trade this whole arrangement is buying.
func (l *Loader) abandon(ctx context.Context, id string) {
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = l.deleteLoad(clean, id)
}

// Swept is what one sweep removed.
type Swept struct {
	// Abandoned names the loads that were left incomplete and have now been
	// removed. It is a list rather than a count because an operator seeing
	// this number climb needs to know which loads, so they can go and find out
	// what is killing them.
	Abandoned []string
}

// Sweep removes loads that started and never finished.
//
// The cutoff is a duration and it is applied by the database, not by this
// process: `now() - interval` rather than a Go-computed instant, for the
// reason pkg/job's store gives at length — with more than one loader, an
// instant computed here means the answer to "is this load stale" depends on
// which machine asked, and that is a bug that presents as work vanishing on
// Tuesdays. It also means a load still in progress on another node is safe as
// long as the cutoff is longer than a load takes.
func (l *Loader) Sweep(ctx context.Context, olderThan time.Duration) (Swept, error) {
	if olderThan <= 0 {
		return Swept{}, fmt.Errorf("pgvector: a sweep cutoff of %v would remove loads that are still running", olderThan)
	}
	var out Swept
	for {
		const sql = `SELECT id FROM {s}.loads
	WHERE state = $1 AND started_at <= now() - ($2::bigint * interval '1 microsecond')
	ORDER BY started_at LIMIT $3 FOR UPDATE SKIP LOCKED`
		rows, err := l.pool.Query(ctx, l.q(sql), stateLoading, olderThan.Microseconds(), l.batch)
		if err != nil {
			return out, fmt.Errorf("pgvector: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return out, fmt.Errorf("pgvector: %w", err)
		}
		for _, id := range ids {
			if err := l.deleteLoad(ctx, id); err != nil {
				return out, err
			}
			out.Abandoned = append(out.Abandoned, id)
		}
		if len(ids) < l.batch {
			return out, nil
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
}

// Load describes one load as the store holds it.
type Load struct {
	ID          string
	Fingerprint string
	Complete    bool
	Dimension   int
	EmbedModel  string
	StartedAt   time.Time
	// Counts is §5's block, as it was returned with the result. It is kept
	// because a graph without the numbers needed to distrust it is the release
	// that section refuses to ship, and a store that drops them on the way in
	// has undone that for every reader downstream.
	Counts []byte
}

// Loads lists what this schema holds, incomplete loads included. They are
// included on purpose: the views hide them from queries, and an operator asking
// what is in the store is precisely the person who should see the one that has
// been loading for six hours.
func (l *Loader) Loads(ctx context.Context) ([]Load, error) {
	const sql = `SELECT id, fingerprint, state = $1, dimension, embed_model, started_at, counts::text
	FROM {s}.loads ORDER BY started_at, id`
	rows, err := l.pool.Query(ctx, l.q(sql), stateComplete)
	if err != nil {
		return nil, fmt.Errorf("pgvector: %w", err)
	}
	defer rows.Close()
	var out []Load
	for rows.Next() {
		var ld Load
		var counts string
		if err := rows.Scan(&ld.ID, &ld.Fingerprint, &ld.Complete, &ld.Dimension, &ld.EmbedModel, &ld.StartedAt, &counts); err != nil {
			return nil, fmt.Errorf("pgvector: %w", err)
		}
		ld.Counts = []byte(counts)
		out = append(out, ld)
	}
	return out, rows.Err()
}
