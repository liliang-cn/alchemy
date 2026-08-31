// Package recall is the read side of what pkg/sink is the write side of, and
// it exists for the same reason one step later.
//
// sink was extracted because four connectors had each invented edge identity,
// provenance handling and a content address on their own. The read side was
// measured in the same way and found in the same state, one step earlier:
//
//	pgvector   Search(vector,k,opts) · Around(hits,depth) · Graph(load)
//	qdrant     Search · Records(Filter,limit) · Findings(loadID) · Count
//	neo4j      nothing
//	cortexdb   Incomplete() only
//
// Two of four had invented a retrieval shape, two had none, and nobody had
// written down what retrieval is. The consequence was not abstract: a ReAct
// agent over a graph in Neo4j had to be given every Cypher query by hand,
// outside the repository, because the connector that holds the graph had no
// way to be asked a question about it.
//
// # The eight, and how each one got here
//
// None of them is designed. The first four are what building one context pack
// by hand needed and nothing else was; the last three were each added after an
// agent answered a question wrongly in a way none of the others could have
// prevented, and the measurement is written down beside the method:
//
//  1. Find an anchor — the entities whose name contains some text. This is
//     where a question enters the graph, and it is the only primitive that
//     takes free text rather than an identifier.
//  2. Walk one hop — every claim adjacent to an entity, each carrying whether
//     it was stated or inferred, who produced it, and a [source#chunk] marker.
//  3. Resolve a citation — the text a claim was extracted from, with its byte
//     range in the original file, so a marker is evidence rather than a label.
//  4. Ask what is unanswered — the duplicate and identity questions touching a
//     subject, so an agent can say "these two may be one thing and nobody has
//     decided" instead of answering as if somebody had.
//  5. Ask what contributed — the sources that had a hand in one node, so a
//     reader can see a join the store MADE and not only the ones it declined.
//  6. Read the vocabulary — the entity types in a load and how many of each,
//     because without it "what is in this graph" is answered by guessing.
//  7. Read out one class — the entities of a type, sized by the count the
//     vocabulary gave, so enumerating is one call rather than the alphabet.
//  8. Read one record — the entity itself, with its attributes, its aliases and
//     the whole of its provenance, because every other method renders and none
//     of them hands back what was stored.
//
// That the list grew is the argument for how it was built rather than against
// it. A read interface designed up front would have had all eight on day one
// and would have had them wrong; every one of the last four exists because a
// specific wrong answer was traced back to a question the graph could not be
// asked. Nothing here was added because it seemed likely to be useful.
//
// A vector search is deliberately not one of them. pgvector's Search and
// Around and Qdrant's Search answer a genuinely different question — "which
// text is about this" — with a genuinely different input, and folding them in
// would mean every store that holds a graph and no embeddings implements a
// method it has to refuse. They stay where they are, on the stores that have
// them, and both surfaces exist.
//
// # Why every method takes the load
//
// It is not tidiness. A store keeps every load it was given, so a corpus
// imported twice is in it twice: one company profile PDF was present under an old
// import with no byte offsets and under the current one, and a citation lookup
// written without a load filter resolved against the wrong import and returned
// the wrong text — under a claim extracted from the right one. Nothing about
// the answer looked wrong.
//
// pgvector's SearchOptions.Loads already states the general form of this: "a
// buyer who re-imports a corpus without deleting the old load has two copies
// of every chunk in the store, and every hit twice … the connector will not
// choose for them". The difference here is that it is a parameter rather than
// an option, and the difference is the whole point: an option has a default
// and the default is where the bug was.
//
// # What a store owes beyond answering
//
//   - A half-written load must not answer. pgvector gets this from its
//     loaded_* views, which hide a load until its last statement commits;
//     Neo4j had no read path and therefore no equivalent, and this package is
//     where it acquires one. A reader that served a load which is still
//     arriving would be reporting a partial graph as a whole one.
//   - The order is part of the answer. A pack built twice from one unchanged
//     load must come out the same, or an agent's cache, a diff between two
//     runs, and a person re-reading yesterday's answer are all comparing
//     shuffles. Each method below says what it orders by.
//   - A citation that does not resolve is an error and never an empty value.
//     See Cite.
//
// A load that is not in the store is nothing at all for the first, second and
// fourth: "no entity in this load is called that" is an ordinary answer, and
// it is the same answer for a load that is empty, a name that is absent, and a
// load that was never imported. Cite is the one that distinguishes them, and
// the reason is that it is the one whose caller was already told the thing
// exists — a claim named that chunk.
package recall

import (
	"context"
	"errors"
)

// ErrNoCitation is returned by Cite for a citation that resolves to nothing in
// the load it was asked for.
//
// It is a sentinel rather than a zero Citation because the two are read
// completely differently by the thing at the other end. An agent handed an
// empty citation has a claim with no text under it and treats it as
// unsupported-but-plausible, which is the failure mode this whole design
// exists to make unreachable; an agent told "this citation does not resolve in
// this load" knows not to offer the claim as evidence.
var ErrNoCitation = errors.New("recall: this citation does not resolve in this load")

// ErrNoChunk is returned by Cite for a claim whose producer did not work in
// chunks. There is nothing to quote and that is not a defect.
//
// It is separate from ErrNoCitation because they say opposite things about how
// far to trust the claim, and conflating them taught an agent to distrust the
// most trustworthy records in the store. Measured: across thirty runs of a
// graph-backed agent, thirteen citation attempts, seven of them against a
// graph-import source whose chunk is -1, and all seven refused with "this
// citation does not resolve, so do not treat it as evidence". Every one of
// those seven was a false alarm — a graph-import claim is a machine reading
// something that already asserted the fact, which §5b ranks ABOVE a model
// reading prose. The agents cited it anyway, being substantively right and
// procedurally wrong, which is a tool teaching its caller to ignore it.
//
// Mark already emits the marker this error belongs to: a claim with chunk -1
// renders as "[team.json]" with no #n, so a caller has no number to pass and
// is not making a mistake by having none. Cite is therefore reached with a
// negative index legitimately, and must say which of the three cases it is —
// here is the text, there is no such chunk, or this claim never had one.
var ErrNoChunk = errors.New("recall: this claim's producer did not work in chunks, so there is no text to quote")

// ErrNoLoad is returned by Cite when the load itself is not in the store, or
// is there and unfinished.
//
// It is separate from ErrNoCitation because they are different mistakes with
// different fixes. A missing chunk is a fact about the data. A missing load is
// a caller naming the wrong import — which is precisely the bug the load
// parameter exists for, arriving as a typo instead of as a silent wrong
// answer.
var ErrNoLoad = errors.New("recall: no finished load of that name is in this store")

// Reader is a store a context pack can be built from.
//
// Every method takes the load first, after the context, and that placement is
// the whole argument of this package: it is a parameter rather than an option
// with a default, because the default is where the bug was. See the package
// doc.
//
// A Reader is safe for concurrent use, because the thing on the other end of
// it is an agent that will ask three of these at once.
type Reader interface {
	// Find returns the entities in this load whose name contains name, case
	// insensitively, ordered by name and then by ID so that a limit cuts the
	// same place twice.
	//
	// It is a substring match and not a similarity search: this is how a
	// question enters the graph, and an agent that asked for "Ravel" and was
	// handed the five nearest names would have no way to tell an exact hit
	// from a neighbour. A store that also does similarity offers it separately
	// and says so.
	//
	// limit must be positive. There is no "everything" value, for the reason
	// pgvector's Search refuses k <= 0: an unbounded anchor search on a
	// four-hundred-thousand-record import is a page nobody reads and a query
	// nobody meant.
	//
	// No match is an empty slice and not an error, including for a load that
	// is not in the store. See the package doc.
	//
	// It returns a Found rather than a slice because the count matters as much
	// as the page. Measured: an anchor search for the name a whole corpus was
	// about matched fourteen entities and returned twelve, and the entity the
	// question was actually about was thirteenth. Two agents on two runtimes
	// were handed the truncated page with no sign it was one, and in seven
	// runs out of eight they went on to invent an id or guess answers from
	// their own prior knowledge -- under a prompt whose first rule was to use
	// only what the tools returned. A page that does not say it is a page asks
	// a reader to trust a list that is not the list.
	Find(ctx context.Context, load, name string, limit int) (Found, error)

	// Claims returns every claim adjacent to the entity with this ID, in
	// either direction, each carrying its own provenance rather than its
	// subject's.
	//
	// Its own is worth saying because getting it wrong is invisible: an edge
	// carries the source, chunk and producer of the assertion, and the node at
	// either end carries the provenance of the sentence that first named it.
	// They are usually different sentences and sometimes different files, and
	// a walk that reported the node's would attribute every claim about an
	// entity to whatever first mentioned it.
	//
	// Both directions in one answer, because an agent asking what is known
	// about a thing does not care which way the extractor happened to write
	// the edge — the same argument pgvector's Around makes for the same
	// choice.
	//
	// Ordered by type, then object, then subject, then source and chunk.
	Claims(ctx context.Context, load, id string) ([]Claim, error)

	// Cite resolves a [source#index] marker to the text it names, within this
	// load.
	//
	// Three outcomes, and telling them apart is the point. The text, when the
	// chunk is there. ErrNoChunk when index is negative, which means the claim
	// came from a producer that did not work in chunks — an ordinary answer,
	// not a failure, and the one Mark's chunk-less marker leads a caller to.
	// ErrNoCitation when the load holds no such chunk, which IS a failure. And
	// ErrNoLoad when there is no finished load of that name. Never a zero
	// Citation for any of them.
	//
	// The middle case was missing and it cost something measurable: with only
	// two outcomes, every chunk-less claim was refused with the sentence
	// reserved for a citation that does not resolve, and an agent was told not
	// to trust the most trustworthy records in the store.
	//
	// Both the source and the index have to match. They are two halves of one
	// marker: a chunk index is unique across a job, so the index alone would
	// resolve, and a caller who passed the wrong file with the right number
	// would be handed text from the other file and no way to notice.
	Cite(ctx context.Context, load, source string, index int) (Citation, error)

	// Unanswered returns the identity questions in this load that mention
	// about, case insensitively, in either name, the subject or the detail.
	//
	// An empty about returns all of them. It is empty rather than a word like
	// "all" because a sentinel that is also a legal search term is a filter
	// that silently stops filtering for one input — and "all" is a plausible
	// name for a system, a table or a column.
	//
	// Ordered by subject and then detail.
	Unanswered(ctx context.Context, load, about string) ([]Question, error)

	// Types is the vocabulary of this load: every entity type in it and how
	// many entities carry it, ordered by type.
	//
	// It is the primitive that was missing when a graph was asked what was in
	// it. Measured: an agent asked "what kinds of things are in this graph, and
	// how many of each" made EIGHTY-THREE tool calls, every one an anchor search
	// for a single letter of the alphabet, and produced a table that got the
	// total right and five things under it wrong -- thirteen types where the
	// load has fourteen, four counts off, and one row reading "1-2" because it
	// could not tell. Asked separately to list every person it named thirteen of
	// twenty-one, having said twenty in that table a moment before. One graph,
	// two runs, two answers that do not agree with each other, neither hedged.
	//
	// The right total is the worst part of it. It is the number a reader would
	// spot-check, and it came out right because the errors cancelled.
	//
	// None of that is the model being careless. Find is a substring search, so
	// the alphabet is genuinely the only way to enumerate with it, and a search
	// for a letter returns a truncated page that reports how many more exist
	// and offers nothing to do about it. A tool that states a number a caller
	// cannot act on has told them their answer is incomplete and left them to
	// state it anyway.
	//
	// An empty load, or one that is not in the store, is an empty slice and not
	// an error, for the reason Find gives.
	Types(ctx context.Context, load string) ([]TypeCount, error)

	// OfType returns the entities in this load of exactly this type, ordered by
	// name and then by ID so that a limit cuts the same place twice.
	//
	// Exactly, and not a substring or a fold: a type is declared by an ontology
	// rather than written by a person, so "Person" and "person" are not the same
	// type and matching them together would report a vocabulary the load does
	// not have. That is the opposite of Find's rule, and deliberately -- Find
	// takes what somebody typed, this takes what Types returned.
	//
	// It is separate from Find rather than a type filter on it because they are
	// different acts. Find is where a question enters the graph and its input is
	// free text; this one takes no text at all and is how a caller reads out a
	// class it already knows exists. Folding them would give Find a second mode
	// reached by leaving its only argument empty, which is the shape of defect
	// Unanswered's "all" already cost thirty runs.
	//
	// limit must be positive, as with Find and for the same reason. A caller who
	// wants all of them asks Types first and passes the count -- which is what
	// the count is for, and why Found.Total is still returned here: a page that
	// does not say it is a page asks a reader to trust a list that is not the
	// list.
	OfType(ctx context.Context, load, typ string, limit int) (Found, error)

	// Describe returns one entity whole: its type, its name, its aliases, its
	// attributes and the whole of its provenance.
	//
	// It is the only method that returns a record rather than an answer, and
	// the only one through which an entity's attributes and an assertion's
	// author and date are reachable at all. Everything else here renders; this
	// one hands back what was stored.
	//
	// An id the load does not hold is a zero Description and a nil error, and an
	// unknown load is ErrNoLoad -- the same asymmetry Contributions draws, for
	// the same reason: a load that is not there is a caller's mistake, and an id
	// that is not there is an ordinary answer.
	Describe(ctx context.Context, load, id string) (Description, error)

	// Contributions reports every source that had a hand in one node, so a
	// reader can see a join the store MADE as well as the ones it declined.
	//
	// Unanswered reports the joins the loader refused; this reports the ones it
	// performed. Without both, only half of identity is visible, and a caller
	// can be cautious only about the half the machine was already unsure of --
	// which is the wrong half, because the other one has already been acted on.
	//
	// It returns ErrNoLoad for an unknown load, and a zero Contributions with a
	// nil error for an id the load does not hold. The asymmetry is deliberate:
	// a load that is not there is a caller's mistake, and an id that is not
	// there is an ordinary answer to "what contributed to this" -- nothing.
	Contributions(ctx context.Context, load, id string) (Contributions, error)
}
