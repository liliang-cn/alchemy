package neo4j

import "errors"

// Defaults chosen so that a caller who fills in only RunID gets a graph that
// is safe rather than fast.
const (
	defaultBaseLabel      = "Alchemy"
	defaultReservedPrefix = "_"
	defaultBatchSize      = 1000
)

var (
	// ErrHeld is returned for a result carrying unanswered conflicts. §7.3: a
	// graph that contradicts itself is worse than no graph.
	ErrHeld = errors.New("neo4j: result is held — a conflict is unanswered")
	// ErrNoRunID is returned when the caller did not name the run. There is no
	// default on purpose; see Options.RunID.
	ErrNoRunID = errors.New("neo4j: Options.RunID is required")
	// ErrRunExists is returned when the named run is already in the graph and
	// was loaded from a different result.
	ErrRunExists = errors.New("neo4j: run already loaded from a different result")
)

// Options configures one Loader.
type Options struct {
	// RunID names the import. It is required and has no default, and that is
	// the single most consequential decision in this package.
	//
	// Entity.ID is stable within one result and says nothing across runs
	// (pkg/alchemy). So a MERGE keyed on the ID alone would fuse run A's "e1"
	// with run B's "e1" — two unrelated things sharing a counter — and produce
	// a graph that looks bigger and truer than the data supports. Keying on
	// (run, id) instead makes a re-load of the same result a no-op and keeps
	// two genuinely different runs apart. Joining nodes across runs is entity
	// resolution, which §5 defers to a second release; a connector that did it
	// on a within-run identifier would be doing it wrong and calling it done.
	//
	// It cannot be defaulted to a generated value, because then a retry after
	// a crash would be indistinguishable from a second import and would double
	// the graph. It cannot be defaulted to a constant either, for the reason
	// above. So the caller says which import this is, which is the one fact
	// only they have.
	RunID string

	// Database is the Neo4j database to write to. Empty means the server's
	// default (Community edition has exactly one).
	Database string

	// BaseLabel is on every node this connector writes, so that "everything
	// alchemy imported" is one label and not a guess, and so that a test — or
	// a buyer undoing an import — has a single handle to delete.
	BaseLabel string

	// ReservedPrefix separates what alchemy knows about a record from what the
	// source said about it. Everything under the prefix is ours; everything
	// else is the model's Attributes, verbatim. It is configurable so that an
	// ontology which genuinely uses underscore-led field names has somewhere
	// to move to other than losing its provenance.
	ReservedPrefix string

	// BatchSize is how many records go in one transaction. §8.4: a large
	// result does not fit in one message and does not fit in one transaction
	// either.
	BatchSize int

	// Overwrite deletes a run that is already present before loading, instead
	// of refusing it. It is the answer to "I know it changed and I mean to
	// replace it", and it is off by default because the same gesture made
	// accidentally is how an import silently rewrites a graph somebody is
	// already querying.
	Overwrite bool

	// SkipChunks leaves Result.Chunks out. The text is usually the largest
	// thing in a result and a buyer who only wants the graph should not pay
	// for the corpus.
	SkipChunks bool

	// SkipFindings leaves violations, duplicates, guesses and unread material
	// out. See findings.go for why they are loaded by default.
	SkipFindings bool
}

// withDefaults fills the blanks. It returns a copy: an Options the caller can
// reuse for a second Loader should not have been mutated by the first.
func (o Options) withDefaults() Options {
	if o.BaseLabel == "" {
		o.BaseLabel = defaultBaseLabel
	}
	if o.ReservedPrefix == "" {
		o.ReservedPrefix = defaultReservedPrefix
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	return o
}
