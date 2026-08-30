// Package sink is the interface DESIGN.md §4 deferred and §4.1 stopped
// deferring.
//
// §4's argument was that "an interface defined before there are two real
// consumers is a guess about a shape, and a wrong interface is harder to change
// than no interface". Four consumers now exist — Neo4j, pgvector, Qdrant and
// CortexDB — each written against the JSON alone with no sight of the others,
// and the wait bought exactly what it was supposed to buy: evidence about which
// parts of a sink are one thing written four times and which are four different
// answers to a question that genuinely has four.
//
// §4.1 states the rule this package is drawn along: what every sink had to
// write for itself belongs above the interface; what they each answered
// differently belongs to the store.
//
// # Above the line, and here
//
//   - Pre-flight refusal. All four wrote it, none wrote all of it, and every
//     omission was a silent overwrite. It is pkg/preflight, and Load asks it
//     before a store is so much as opened.
//   - Result identity. Two demanded a run ID from their caller and two invented
//     a content fingerprint, so "have I loaded this already" had four answers.
//     It is Ident.Load and Ident.Digest, and Digest is the one content address.
//   - The streaming envelope. Begin, then records, then Commit or Abort.
//   - The report, including Lost.
//   - Convergence: the same result under one name is a no-op, a different one
//     is refused unless the caller says to replace it, and a load that failed
//     part-way through is observable as unfinished rather than merely unlikely.
//
// # Below the line, and deliberately absent
//
// Idempotency mechanics — a MERGE on a key and an ON CONFLICT DO UPDATE have
// nothing in common but the word. Options structs, dimension binding, index
// policy, the query surface, and any reserved-prefix scheme: Neo4j needs one
// because its properties are flat, Qdrant's nested payload makes a collision
// unreachable, and pushing one on both would be the guess §4 was right to
// defer. Every one of those stays in the connector that has an opinion.
//
// # Why Begin/stream/Commit rather than Load(Result)
//
// §8.4 pages a result over gRPC precisely because a large one does not fit in
// one message — and the four connectors were then handed a fully materialised
// alchemy.Result, so a four-hundred-thousand-record graph sat in one process's
// heap before a byte reached the store. Two of them said so in comments. The
// envelope is the shape that lets a paged stream become a load without ever
// being a struct.
//
// Load, below, is the adapter for the caller that does hold a whole result,
// which is every caller today. It is a driver over this interface and not a
// second way in: what it does is exactly what a paged reader would do, in the
// order §8.4 already sends.
package sink

import (
	"context"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Ident is what a load is called, what it is of, and what a store has to know
// before it can hold anything.
//
// It is the whole of what Begin gets, and each field is here because all four
// stores needed it and none could derive it.
type Ident struct {
	// Load is the name this graph is filed under. It is alchemy.Result.Job when
	// the producer named one, so a retry is a retry (§8.3) rather than a second
	// import; see Options.Load for the caller who wants to override it.
	Load string
	// Digest is the content address of the whole result: what decides whether a
	// load already present under this name is this graph or another one. See
	// Digest for what it covers.
	Digest string
	// Replace says the caller means to overwrite a load of the same name that
	// holds a different graph. Without it Begin refuses, because two different
	// things under one name is a question and nothing in the data answers it.
	Replace bool
	// Vectors is the width and the model of this result's embeddings, or the
	// zero value when it has none.
	//
	// It is on the envelope rather than discovered from the first record
	// because two of the four stores cannot change it afterwards: Qdrant fixes
	// the width when the collection is created and has no ALTER, and a store
	// that learned it from record one would have to create the collection in
	// the middle of a write.
	Vectors Vectors
}

// Vectors is one result's embedding width and the model that produced it.
type Vectors struct {
	// Dimension is 0 when the result carries no vectors, which is a real
	// answer and not a missing one: a schema import has none, and a store told
	// 0 should create no vector space rather than guess a width.
	Dimension int
	Model     string
}

// Chunk is one span of source text together with its embedding.
//
// They travel as one record, and that is the single most useful thing the
// envelope does to the shape. alchemy.Vector points at a chunk by index, and
// every store built a map to join them — which is where "does every vector name
// a chunk that exists" came from, why two stores checked it and two did not,
// and why none of them noticed that two vectors naming one chunk silently kept
// the last. Paired, the question cannot be asked.
//
// Vector is nil for a chunk nobody embedded, which is an ordinary thing: §5c
// puts the embedding after review, so a chunk that was rejected or produced
// nothing legitimately has none and keeps its text.
type Chunk struct {
	alchemy.Chunk
	Vector []float32
	// Model is the embedder that produced Vector, empty when there is none.
	Model string
}

// Findings is what a job found wrong with the records, streamed beside them.
//
// They are here rather than in Summary because they are per-item and one of the
// four stores makes a node of each; a store that wants them in one blob at the
// end can accumulate, and they are small by construction — a violation per
// broken record, a guess per mapped column, an unread per page nobody could
// read.
type Findings struct {
	Violations []alchemy.Violation
	Duplicates []alchemy.Duplicate
	Guesses    []alchemy.Guess
	Unread     []alchemy.Unread
}

// Summary is §5's obligation: "every returned graph is accompanied by the
// numbers needed to distrust it". It reaches the store once, at Commit.
//
// Conflicts are in it rather than in Findings because a load never carries an
// open one — §7.3 holds the job, and Load refuses before Begin — so what
// travels here is the record of a question that was asked and answered, which
// is a fact about the run rather than about a record.
type Summary struct {
	Counts     alchemy.Counts
	Conflicts  []alchemy.Conflict
	RuleSets   []alchemy.RuleSet
	ModelCalls []alchemy.ModelCall
}

// Loss is one thing a store could not represent.
//
// §4.1 puts it above the line although only one of the four needed it: a vector
// store cannot hold a traversal, so loading a graph into Qdrant loads its
// records and not its shape, and a connector that returned success without
// saying so would be lying by omission. It is in the interface because a
// guarantee that only holds where it is convenient is not a guarantee, and
// because the next store nobody has thought of will need it too.
type Loss struct {
	// What names the part of the model that did not survive, in the words the
	// contract uses for it: "relations", "vectors", "chunks".
	What  string
	Count int
	// Why is the store's own reason, in a sentence a person can act on. It is
	// the store's rather than the interface's because only the store knows what
	// it cannot hold.
	Why string
}

// Report is what a load did.
//
// The record counts are the driver's and the rest is the store's, and the split
// is what makes a failed load reportable: Load counts what it handed over, so a
// load that died halfway still says how much of it landed, where a store asked
// to recount would be answering from a view it only half has. What Commit
// contributes is what only the store knows — how many round trips it took, what
// it could not keep, and the name it resolved the load to.
type Report struct {
	Load   string
	Digest string
	// Converged is true when the store already held this exact graph under this
	// name, so nothing was written. It is not an error: it is what makes a
	// retried nightly import cost nothing.
	Converged bool

	Entities   int
	Relations  int
	Chunks     int
	Vectors    int
	Violations int
	Duplicates int
	Guesses    int
	Unread     int

	// Batches is how many round trips it took, which is the number an operator
	// needs when a load dies halfway.
	Batches int
	// Lost is what the store could not keep. Empty is a store that kept
	// everything, which is a claim rather than a silence.
	Lost []Loss
}

// Sink is a store a graph can be loaded into.
//
// It has one method because everything else a store does — how it batches, what
// it indexes, how it answers a query, what it reserves — is the store's own and
// §4.1 says so. What it owes a caller is the ability to begin a load under an
// identity.
type Sink interface {
	// Begin opens a load. It is where a store binds whatever it has to bind
	// before it can hold anything (a vector width, a collection, a schema) and
	// where it decides what an existing load of the same name means.
	//
	// It refuses with ErrExists when the name is taken by a different graph and
	// Ident.Replace is not set. A load that is already present and identical
	// returns a Tx whose Converged reports true.
	Begin(ctx context.Context, id Ident) (Tx, error)
}

// Tx is one load in progress.
//
// The order is a contract and not a convenience: entities before the relations
// that name them, because every one of the four decides what to do with an edge
// by asking whether both its ends are there, and a store that met the edge
// first would have to buffer the graph to answer — which is the thing this
// envelope exists to avoid.
//
// Every method may be called any number of times, including none. A Tx is used
// by exactly one goroutine.
type Tx interface {
	// Converged reports that the store already holds this graph under this
	// name, so the writes below need not do anything. It is a question the
	// envelope asks and an answer the store gives, because the mechanics differ
	// completely: one store short-circuits, another re-runs its MERGEs because
	// that is how a crashed load is finished.
	Converged() bool

	Entities(ctx context.Context, batch []alchemy.Entity) error
	Relations(ctx context.Context, batch []alchemy.Relation) error
	Chunks(ctx context.Context, batch []Chunk) error
	Findings(ctx context.Context, f Findings) error

	// Commit writes the summary and marks the load finished. Until it returns,
	// a reader of the store must be able to see that this load is unfinished.
	//
	// The Report it returns is the store's half: Batches, Lost, Converged, and
	// Load where the store resolved this graph to a name other than the one it
	// was given. The record counts are filled in by the driver and anything set
	// here is overwritten, because a count is a fact about what was handed over
	// and the driver is what handed it.
	Commit(ctx context.Context, s Summary) (Report, error)
	// Abort ends a load without finishing it. It does not have to remove what
	// was written — a half-written load that says it is half-written is what
	// §8.3's takeover needs to find — but it must leave the store saying so.
	Abort(ctx context.Context) error
}
