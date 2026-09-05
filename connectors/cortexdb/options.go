package cortexdb

import "errors"

// Defaults chosen so that a caller who fills in only RunID gets a store that
// is honest rather than fast.
const (
	// defaultCollection is where the chunk embeddings go. It is a name of our
	// own rather than CortexDB's "graphrag_chunks", because that collection
	// holds vectors CortexDB computed with its own lexical hash and this one
	// holds vectors a real embedding model produced. Mixing them would put two
	// incomparable geometries in one similarity search.
	defaultCollection = "alchemy_chunks"
	// defaultReservedPrefix separates what alchemy knows about a record from
	// what the source said about it.
	defaultReservedPrefix = "_"
	defaultBatchSize      = 500
)

var (
	// ErrHeld is returned for a result carrying unanswered conflicts. §7.3: a
	// graph that contradicts itself is worse than no graph.
	ErrHeld = errors.New("cortexdb: result is held — a conflict is unanswered")
	// ErrNoRunID is returned when the caller did not name the run. There is no
	// default on purpose; see Options.RunID.
	ErrNoRunID = errors.New("cortexdb: Options.RunID is required")
	// ErrRunExists is returned when the named run is already in the store and
	// was loaded from a different result.
	ErrRunExists = errors.New("cortexdb: run already loaded from a different result")
	// ErrParallelEdges is returned when two alchemy relations that the
	// producer named as different edges would land on one CortexDB edge. See
	// Options.FuseParallelEdges.
	ErrParallelEdges = errors.New("cortexdb: two keyed relations collide on one CortexDB edge")
	// ErrStrictOntology is returned when the target store's active schema
	// enforces its property list, which leaves alchemy's provenance nowhere to
	// go. See Loader.checkStore.
	ErrStrictOntology = errors.New("cortexdb: the store's active ontology is strict and declares none of alchemy's provenance properties")
	// ErrRekeyed is returned when the target store's active ontology put the
	// entity nodes somewhere other than where this run asked for them. See
	// Loader.Load.
	ErrRekeyed = errors.New("cortexdb: the store's active ontology re-keyed this run's entities")
	// ErrEdgeUnknown is returned when a `_contradicts` this load wrote names an
	// edge id the store did not report using. An edge's identity is CortexDB's
	// and this connector predicts it in order to write the knowledge contract's
	// key; the prediction is checked rather than trusted. See
	// Loader.writeRelations and plan.edgeRecordID.
	ErrEdgeUnknown = errors.New("cortexdb: _contradicts names an edge the store did not write")
)

// Options configures one Loader.
type Options struct {
	// RunID names the import. It is required and has no default, for the same
	// reason it is required in the Neo4j connector: Entity.ID is stable within
	// one result and says nothing across runs, so every node this connector
	// writes is keyed on (run, id) and nothing is ever merged across runs.
	//
	// It matters more here than there. CortexDB's own entity identity is the
	// entity's *name*, folded for case and punctuation — which is entity
	// resolution, and a good answer to the question CortexDB is asking, where
	// two documents mentioning "Ravel" mean one Ravel. It is the wrong
	// answer to the question alchemy is asking: §5 defers entity resolution to
	// a second release, and a connector that let the store silently join two
	// imports on a folded name would be doing it anyway and calling it done.
	// Passing an "entity:"-prefixed node ID is how CortexDB lets a caller say
	// "I have already decided identity", and this connector always does.
	RunID string

	// Collection is the vector collection the chunk embeddings go into.
	Collection string

	// ReservedPrefix separates alchemy's own metadata keys from the source's
	// attributes. Everything under the prefix is ours; everything else is what
	// the model said, verbatim.
	ReservedPrefix string

	// BatchSize is how many records go in one CortexDB batch. §8.4: a large
	// result does not fit in one message and does not fit in one write either.
	BatchSize int

	// SkipChunks leaves Result.Chunks and Result.Vectors out. The text and the
	// embeddings are the largest thing in a result, and a caller who keeps
	// their vectors in the store they bought for vectors should not pay for
	// them twice.
	//
	// It also removes every chunk id from the graph, and with it the mention
	// edges — so CortexDB's fact_provenance can still say which document a fact
	// came from and no longer which words. That is a real loss, and the report
	// says so (Report.Chunks and Report.MentionEdges come back nought) rather
	// than leaving it to be understood.
	SkipChunks bool

	// FuseParallelEdges allows two relations the producer named as different
	// edges (Relation.Key) to be written as the one CortexDB edge their
	// identity rule makes them.
	//
	// Off by default because the alternative is that one of the two wins
	// silently — the same reason the Neo4j connector refuses an attribute that
	// collides with alchemy's own namespace. When it is on, every fusion is
	// named in Report.FusedRelations, so the loss is at least attributable.
	FuseParallelEdges bool
}

func (o Options) withDefaults() Options {
	if o.Collection == "" {
		o.Collection = defaultCollection
	}
	if o.ReservedPrefix == "" {
		o.ReservedPrefix = defaultReservedPrefix
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	return o
}
