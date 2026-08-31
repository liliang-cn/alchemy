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
	"github.com/liliang-cn/alchemy/pkg/preflight"
	"github.com/liliang-cn/alchemy/pkg/sink"
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
	// Supersessions is how many retirements were recorded beside the graph. It
	// counts claims filed and never rows removed: this connector writes what a
	// result says is over and deletes nothing, so a load reporting 12 holds
	// exactly as many entities and relations as it would have without them.
	Supersessions int
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
// The test for "unanswered" is alchemy.Result.Held, asked of the result rather than reimplemented.
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

// Unwrap makes this store's sentence and the envelope's question one error.
//
// sink.ErrExists is what a caller asks when it does not care which store
// answered — "the name holds a different graph" is the same fact everywhere —
// and this type is what a caller asks when it wants the two fingerprints and
// the state. Wrapping rather than replacing keeps both: errors.Is finds the
// sentinel, errors.As finds the detail, and neither reader had to change.
func (e *ConflictingLoadError) Unwrap() error { return sink.ErrExists }

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

// DuplicateEntityError is a result in which two records claim one entity ID and
// describe DIFFERENT nodes.
//
// alchemy.Entity.ID "is how relations refer to entities", so two different
// nodes under one ID make every relation naming it ambiguous. That is a broken
// result rather than a storage problem, and the connector says so instead of
// picking a winner: a store that kept the last one would silently answer with a
// graph whose edges point at the wrong node.
//
// Two records that AGREE are not this. They are one node asserted by two
// sources, which is corroboration and the ordinary shape of a merged corpus;
// pkg/sink folds them into one row and reports the count. This error was raised
// for both until the rule moved into pkg/preflight, and for as long as it was,
// no graph built from more than one document could be loaded here at all.
type DuplicateEntityError struct {
	ID string
}

func (e *DuplicateEntityError) Error() string {
	return fmt.Sprintf("pgvector: two entities in this result claim the ID %q and are not the same node, and "+
		"relations refer to entities by ID; nothing was written, because either one of them would make some "+
		"edge point at the wrong node. Two records that agree about the type and the name are corroboration "+
		"and are loaded", e.ID)
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
	// This connector's own refusals first, so that everything it has ever
	// answered with a typed error still answers with one. §4.1 moved the
	// *shared* refusals above the line, not this store's account of them: a
	// caller matching on *DuplicateEntityError or *DimensionError is matching
	// on this package's contract, and an interface extraction that quietly
	// changed which error a known input produces would be a redesign wearing a
	// refactor's clothes.
	if held := res.Held(); len(held) > 0 {
		return Loaded{}, &HeldError{Conflicts: held}
	}
	if err := checkEntityIDs(res); err != nil {
		return Loaded{}, err
	}
	if _, _, err := dimensionOf(res); err != nil {
		return Loaded{}, err
	}

	// And then the envelope, which asks pkg/preflight before it opens
	// anything, derives the identity, and streams. Everything below this line
	// used to be two hundred lines of this file and is now the same two hundred
	// lines in one place, shared with three other stores.
	rep, err := sink.Load(ctx, l, res, sink.Options{
		Load: opts.ID, Replace: opts.Replace, Batch: l.batch,
	})
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{
		ID: rep.Load, Fingerprint: rep.Digest, Already: rep.Converged,
		// Dimension is read back off the result rather than off the report,
		// because it is what this store bound and a converged load bound
		// nothing.
		Dimension:     l.BoundDimension(ctx),
		Entities:      len(res.Entities),
		Relations:     len(res.Relations),
		Chunks:        len(res.Chunks),
		Vectors:       len(res.Vectors),
		Violations:    len(res.Violations),
		Duplicates:    len(res.Duplicates),
		Supersessions: len(res.Supersessions),
	}, nil
}

// Fingerprint is the content address of a whole result.
//
// It is pkg/sink's now. The function stays because it is this package's API and
// a buyer may have written it into their own idempotency check, and it
// delegates rather than keeping a second implementation: two content addresses
// for one result is exactly the divergence §4.1 says four connectors produced,
// preserved for compatibility inside one of them.
//
// The value it returns is not the value it returned before this became shared,
// and that is a deliberate one-time break rather than a drift. Nothing persists
// a fingerprint outside a store's own load row, a re-load under the new address
// is a new load rather than a corruption, and the new one is better in two ways
// the old one was wrong about: it is order-independent, so a result reassembled
// from §8.4's pages is the same load, and it covers the chunks, the vectors,
// the counts and the policy, all of which this store writes.
func Fingerprint(res alchemy.Result) (string, error) { return sink.Digest(res), nil }

// checkEntityIDs refuses the ID collisions that are collisions, and it asks
// pkg/preflight rather than deciding.
//
// It used to hold its own rule -- an ID seen twice is an error -- and that rule
// was right until fb437ce, which legalised two records under one ID that AGREE
// about what the node is. The connectors were not touched by that commit, so
// the one thing this product exists to produce, a graph merged from several
// sources, still could not be loaded here: two documents each asserting
// "LINSTOR controller is a Component" were refused as a broken result.
//
// Asking preflight is the fix and not a refactor. A store deciding for itself
// which graphs are writable, while the envelope it calls decides again with a
// different rule, is two answers to one question -- and the store's answer wins
// because it runs first, which is how a rule change landed in the core and
// changed nothing here.
//
// The typed error stays, because a caller matching on *DuplicateEntityError is
// matching on this package's contract. What changed is which inputs produce it:
// only records that disagree. An entity with no ID at all is preflight's to
// report -- it is the same Kind but a different mistake, and its own message
// says so better than this one's could.
func checkEntityIDs(res alchemy.Result) error {
	for _, d := range preflight.Check(res) {
		if d.Kind == preflight.EntityIDReused && d.Subject != "" {
			return &DuplicateEntityError{ID: d.Subject}
		}
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

// claim writes the load row, in its own transaction, before any data.
//
// This row is what makes a half-written load detectable rather than merely
// unlikely: it exists from the first byte, it says `loading`, and every read
// view joins against it. A connector that wrote the marker last would have a
// window in which the rows are there and nothing knows they are partial —
// which is the one outcome worth this much machinery to avoid.
func (l *Loader) claim(ctx context.Context, id, digest string, dim int, replace bool) error {
	if replace {
		if err := l.deleteLoad(ctx, id); err != nil {
			return err
		}
	}
	const sql = `INSERT INTO {s}.loads (id, fingerprint, state, dimension, embed_model)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING RETURNING id`
	var got string
	err := l.pool.QueryRow(ctx, l.q(sql), id, digest, stateLoading, dim, "").Scan(&got)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pgvector: %w", err)
	}
	// Nothing inserted: the ID is taken. Which graph is under it decides
	// whether this is a retry or a collision.
	var have, state string
	err = l.pool.QueryRow(ctx, l.q(`SELECT fingerprint, state FROM {s}.loads WHERE id = $1`), id).
		Scan(&have, &state)
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}
	return &ConflictingLoadError{ID: id, Have: have, Want: digest, State: state}
}

// complete is the last statement of a load and the only one that makes it
// visible. The summary blocks are written here rather than at claim time
// because a load row that carried §5's counts while its rows were still
// arriving would be a store advertising numbers it could not yet answer with.
func (l *Loader) complete(ctx context.Context, id string, s sink.Summary, guesses []alchemy.Guess, unread []alchemy.Unread) error {
	blocks := []any{s.Counts, s.RuleSets, s.Conflicts, guesses, unread, s.ModelCalls}
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
