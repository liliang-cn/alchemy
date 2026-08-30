package pgvector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// LoadOptions is what a caller can say about one load beyond the result
// itself.
type LoadOptions struct {
	// ID names this load. Empty derives it from the fingerprint, which is the
	// answer that needs no coordination: the same result always lands under
	// the same name and a different one never does.
	//
	// Supplying one is for the caller who has a better name — an alchemy job
	// ID, a nightly run's date — and who is then responsible for not reusing
	// it for a different graph. Doing so is refused rather than merged.
	ID string
	// Replace deletes whatever is under ID and loads this result in its place.
	// It is how a caller re-runs a corpus under a stable name, and how a load
	// that died halfway is retried, so it is explicit rather than a default:
	// a connector that replaced by default would make "load the second half of
	// the corpus" delete the first.
	Replace bool
}

// Loaded is what one load did.
type Loaded struct {
	ID          string
	Fingerprint string
	// Already is true when this exact result was already in the store and
	// nothing was written. It is a field rather than an error because a
	// retried nightly job doing nothing is a success, and a caller that has to
	// distinguish an error it should ignore from one it should not is a caller
	// that will get it wrong.
	Already bool
	// Dimension is the width of this result's vectors, 0 when it had none.
	Dimension  int
	Entities   int
	Relations  int
	Chunks     int
	Vectors    int
	Violations int
	Duplicates int
}

// HeldError refuses a result that still carries an unanswered conflict.
//
// §7.3: "a graph that contradicts itself is worse than no graph, because an
// agent reading it will answer from whichever edge it happened to traverse —
// confidently, with a citation. The contradiction does not surface at the
// moment of harm." A connector is the exact place that harm becomes possible,
// because it is the step after which the graph is no longer a job somebody is
// watching but a store somebody queries.
//
// The test for "unanswered" is review.Held, imported rather than reimplemented.
// A second, differently-worded copy of that rule in this module would be a
// second answer to the question of when a job is finished, and the two would
// disagree the first time either moved.
type HeldError struct {
	Conflicts []alchemy.Conflict
}

func (e *HeldError) Error() string {
	subjects := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		subjects = append(subjects, fmt.Sprintf("%s (%s)", c.Subject, c.Kind))
	}
	return fmt.Sprintf("pgvector: this result is held for a person: %d conflict(s) are unanswered — %s. "+
		"Nothing was written. Answer them on the Review stream first; a store that took the graph anyway "+
		"would answer questions from whichever side of the contradiction it happened to traverse",
		len(e.Conflicts), strings.Join(subjects, "; "))
}

// ConflictingLoadError is one name over two different graphs.
//
// It is an error rather than a merge or an overwrite because Entity.ID says
// nothing across runs: there is no key on which the two could be joined, and
// picking one silently would be the store deciding a question the data does
// not answer. The caller knows which they meant, so the caller is asked.
type ConflictingLoadError struct {
	ID    string
	Have  string // the fingerprint already under that ID
	Want  string // this result's fingerprint
	State string
}

func (e *ConflictingLoadError) Error() string {
	if e.Have == e.Want {
		return fmt.Sprintf("pgvector: load %q is already %s with this same result; "+
			"if it died halfway, load it again with Replace to take the name over", e.ID, e.State)
	}
	return fmt.Sprintf("pgvector: load %q already holds a different result (%s… vs %s…) and is %s. "+
		"Nothing was written. Entity IDs are stable within one result and mean nothing across runs, "+
		"so these two graphs cannot be merged on anything; give this one another ID, or pass Replace to mean it",
		e.ID, e.Have[:12], e.Want[:12], e.State)
}

// DuplicateEntityError is a result whose own entity IDs collide.
//
// alchemy.Entity.ID "is how relations refer to entities", so two entities under
// one ID make every relation naming it ambiguous. That is a broken result
// rather than a storage problem, and the connector says so instead of picking
// a winner: a store that kept the last one would silently answer with a graph
// whose edges point at the wrong node.
type DuplicateEntityError struct {
	ID string
}

func (e *DuplicateEntityError) Error() string {
	return fmt.Sprintf("pgvector: two entities in this result have the ID %q, and relations refer to entities "+
		"by ID; nothing was written, because either one of them would make some edge point at the wrong node", e.ID)
}

// Load writes one result into the store.
//
// The order of the steps is the design. Everything that can refuse — the held
// conflict, the colliding entity IDs, the dimension — happens before any row
// exists, so a refusal leaves the store exactly as it found it. Everything
// that writes happens under a load row that is not yet complete, so a failure
// after that point leaves data that no read can see. There is no window in
// which a query gets half a graph.
func (l *Loader) Load(ctx context.Context, res alchemy.Result, opts LoadOptions) (Loaded, error) {
	if held := review.Held(res); len(held) > 0 {
		return Loaded{}, &HeldError{Conflicts: held}
	}
	if err := checkEntityIDs(res); err != nil {
		return Loaded{}, err
	}
	dim, model, err := dimensionOf(res)
	if err != nil {
		return Loaded{}, err
	}
	fp, err := Fingerprint(res)
	if err != nil {
		return Loaded{}, err
	}
	out := Loaded{
		Fingerprint: fp, Dimension: dim,
		ID:         opts.ID,
		Entities:   len(res.Entities),
		Relations:  len(res.Relations),
		Chunks:     len(res.Chunks),
		Vectors:    len(res.Vectors),
		Violations: len(res.Violations),
		Duplicates: len(res.Duplicates),
	}
	if out.ID == "" {
		// Deriving the default name from the fingerprint makes the primary key
		// itself carry the idempotency, so a re-load collides in the database
		// rather than relying on a check that could be racing.
		out.ID = "ld_" + fp[:24]
	}

	if !opts.Replace {
		if id, ok, err := l.completeFingerprint(ctx, fp); err != nil {
			return Loaded{}, err
		} else if ok {
			out.ID, out.Already = id, true
			return out, nil
		}
	}
	// The dimension is bound before the load row is written so that a refused
	// dimension leaves no row behind. It is DDL, so it is outside the load's
	// transactions by necessity as well as by choice.
	if err := l.bindDimension(ctx, dim, model); err != nil {
		return Loaded{}, err
	}
	if err := l.claim(ctx, out, opts.Replace); err != nil {
		if already, ok, e2 := l.alreadyComplete(ctx, out.ID, fp, err); e2 == nil && ok {
			out.ID, out.Already = already, true
			return out, nil
		}
		return Loaded{}, err
	}

	if err := l.write(ctx, out.ID, res, dim); err != nil {
		l.abandon(ctx, out.ID)
		return Loaded{}, err
	}
	if err := l.complete(ctx, out.ID, res); err != nil {
		l.abandon(ctx, out.ID)
		// A concurrent loader that committed the identical result first wins
		// the unique index. Its graph is this graph, so the right answer is
		// the same one a sequential second load gets.
		if id, ok, e2 := l.completeFingerprint(ctx, fp); e2 == nil && ok && isUnique(err) {
			out.ID, out.Already = id, true
			return out, nil
		}
		return Loaded{}, err
	}
	return out, nil
}

// checkEntityIDs is a pass over the result the type system does not do.
// Entity.ID is documented as "stable within one result", which a caller can
// read as a promise and a producer can break.
func checkEntityIDs(res alchemy.Result) error {
	seen := make(map[string]bool, len(res.Entities))
	for _, e := range res.Entities {
		if seen[e.ID] {
			return &DuplicateEntityError{ID: e.ID}
		}
		seen[e.ID] = true
	}
	return nil
}

// completeFingerprint finds a finished load carrying this exact graph.
func (l *Loader) completeFingerprint(ctx context.Context, fp string) (string, bool, error) {
	var id string
	err := l.pool.QueryRow(ctx, l.q(`SELECT id FROM {s}.loads WHERE fingerprint = $1 AND state = $2`),
		fp, stateComplete).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pgvector: %w", err)
	}
	return id, true, nil
}

// alreadyComplete turns "that ID is taken by this same graph, and it finished"
// into the no-op a retry deserves. Any other collision stays an error.
func (l *Loader) alreadyComplete(ctx context.Context, id, fp string, cause error) (string, bool, error) {
	var ce *ConflictingLoadError
	if !errors.As(cause, &ce) || ce.Have != fp || ce.State != stateComplete {
		return "", false, cause
	}
	return id, true, nil
}

// claim writes the load row, in its own transaction, before any data.
//
// This row is what makes a half-written load detectable rather than merely
// unlikely: it exists from the first byte, it says `loading`, and every read
// view joins against it. A connector that wrote the marker last would have a
// window in which the rows are there and nothing knows they are partial —
// which is the one outcome worth this much machinery to avoid.
func (l *Loader) claim(ctx context.Context, out Loaded, replace bool) error {
	if replace {
		if err := l.deleteLoad(ctx, out.ID); err != nil {
			return err
		}
	}
	const sql = `INSERT INTO {s}.loads (id, fingerprint, state, dimension, embed_model)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING RETURNING id`
	var id string
	err := l.pool.QueryRow(ctx, l.q(sql), out.ID, out.Fingerprint, stateLoading, out.Dimension, "").Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pgvector: %w", err)
	}
	// Nothing inserted: the ID is taken. Which graph is under it decides
	// whether this is a retry or a collision.
	var have, state string
	err = l.pool.QueryRow(ctx, l.q(`SELECT fingerprint, state FROM {s}.loads WHERE id = $1`), out.ID).
		Scan(&have, &state)
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}
	return &ConflictingLoadError{ID: out.ID, Have: have, Want: out.Fingerprint, State: state}
}

// complete is the last statement of a load and the only one that makes it
// visible. The summary blocks are written here rather than at claim time
// because a load row that carried §5's counts while its rows were still
// arriving would be a store advertising numbers it could not yet answer with.
func (l *Loader) complete(ctx context.Context, id string, res alchemy.Result) error {
	blocks := []any{res.Counts, res.RuleSets, res.Conflicts, res.Guesses, res.Unread, res.ModelCalls}
	args := []any{id, stateComplete, stateLoading}
	for _, b := range blocks {
		raw, err := json.Marshal(b)
		if err != nil {
			return fmt.Errorf("pgvector: %w", err)
		}
		args = append(args, string(raw))
	}
	const sql = `UPDATE {s}.loads SET state = $2, completed_at = now(),
	counts = $4::jsonb, rule_sets = $5::jsonb, conflicts = $6::jsonb,
	guesses = $7::jsonb, unread = $8::jsonb, model_calls = $9::jsonb,
	embed_model = coalesce(nullif((SELECT max(embed_model) FROM {s}.chunks WHERE load_id = $1), ''), embed_model)
WHERE id = $1 AND state = $3`
	tag, err := l.pool.Exec(ctx, l.q(sql), args...)
	if err != nil {
		return fmt.Errorf("pgvector: completing load %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("pgvector: load %s was not in state %s when it finished; "+
			"something else changed it underneath, and it has been left invisible rather than published", id, stateLoading)
	}
	return nil
}

// isUnique reports whether an error is a unique-constraint violation, which is
// how a race between two loaders of the same graph presents.
func isUnique(err error) bool {
	var pge *pgconn.PgError
	return errors.As(err, &pge) && pge.Code == "23505"
}
