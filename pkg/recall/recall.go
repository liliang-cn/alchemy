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
// # The four, and why exactly these four
//
// They are not designed. They are what building one context pack by hand
// needed, and nothing else was needed:
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
	"fmt"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
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

// Found is a page of anchors and how many there were.
//
// Total is the number of matches, which may exceed len(Nodes). A consumer that
// ignores it gets what it always got; one that reads it can say "fourteen
// matched, twelve shown" and let a person or a model narrow the search instead
// of reasoning over a silently truncated list as though it were complete.
type Found struct {
	Nodes []Node
	Total int
}

// Truncated reports whether the page leaves matches unshown. It is a method
// rather than a field so it cannot fall out of step with the two numbers it is
// derived from.
func (f Found) Truncated() bool { return f.Total > len(f.Nodes) }

// Node is an entity as an anchor: the least a caller needs to ask the next
// question.
//
// It is not alchemy.Entity, and the omissions are the point. Attributes,
// aliases and provenance are what an entity *is*; this is what an anchor
// search returns twenty of, and a search that returned twenty entities with
// their attribute maps would be a page of JSON where a person wanted a list of
// candidates. ID is here because it is the argument the next call takes.
type Node struct {
	ID   string
	Type string
	Name string
}

// Claim is one adjacent assertion, in the terms a reader has to weigh it in.
//
// It is names rather than IDs on both ends, because a claim is read by a
// person or a model and "e17 -[USES]-> e04" is not a claim anybody can weigh.
// The ID stays available through Find, which is where an identifier is the
// thing being asked for.
//
// Everything else on it is §5b's guarantee delivered at the point it is
// actually used: "every entity and relation can name the source, the chunk and
// the producer it came from" is a promise about the write path until somebody
// reads one back with those fields attached.
type Claim struct {
	// From, Type and To are the assertion, named the way the edge is: subject,
	// relationship type, object. The direction is the extractor's and is not
	// normalised — a claim read back pointing the other way is a different
	// claim.
	From string
	Type string
	To   string
	// FromID and ToID are the same two ends as the argument the next question
	// takes, and they are here because without them a walk cannot continue.
	//
	// Measured, on the question this interface exists to answer: an agent asked
	// which products a team's people work on made thirteen tool calls, and eight
	// of them were four Find/Claims pairs — one anchor search per neighbour,
	// spent entirely on turning a name this method had just returned back into
	// the ID this method requires. The names are what a claim is read in and
	// stay the fields a reader sees; the IDs are what it is walked by.
	//
	// It is not a convenience. A name is not a key: Find is a substring search
	// whose page can be truncated, so the round trip an agent has to make can
	// return two candidates, or the wrong one, or -- when a corpus is about the
	// name being searched for -- a page that does not contain the node at all.
	// A walk that has to guess its way back to an identifier it was already
	// holding is a walk that can silently continue from the wrong node.
	FromID string
	ToID   string
	// Stated says the producer read something that already asserted this,
	// rather than inferring it. It is computed by NewClaim through
	// alchemy.Producer.Deterministic and never re-implemented here or in a
	// connector; see NewClaim.
	Stated bool
	// Producer is what made it, unabridged. Stated is the split a reader acts
	// on and this is what they check when the split is not enough — "a model
	// proposed it" and "a table's header produced it under a mapping that was
	// itself guessed" are both inferred and are not the same claim.
	Producer alchemy.Producer
	// Source and Chunk are the citation this claim can be checked against.
	// Chunk is -1 when the producer did not work in chunks, which is a fact
	// about the record rather than a missing value; see Mark.
	Source string
	Chunk  int
}

// NewClaim assembles a claim from an edge and the provenance the store had on
// it, and it is the only way a Claim should be built.
//
// It exists to hold one decision in one place. Stated is
// alchemy.Producer.Deterministic and nothing else — not a list of producer
// names copied into a connector, and not the boolean the store materialised at
// load time. Both stores do materialise it (Neo4j's `_deterministic` property,
// pgvector's prov_deterministic column) and both are right to: those exist so
// a buyer can write "the half that was guessed" as their own WHERE clause
// without owning the rule. Reading one back is a different act. The stored
// value is the answer the rule gave on the day of the import, and a graph
// loaded a year ago would answer with the rule as it stood a year ago, in a
// field an agent is about to decide how much to trust a sentence on.
func NewClaim(from, to Endpoint, typ string, p alchemy.Provenance) Claim {
	return Claim{
		From: from.Name, Type: typ, To: to.Name,
		FromID:   from.ID,
		ToID:     to.ID,
		Stated:   p.Producer.Deterministic(),
		Producer: p.Producer,
		Source:   p.Source,
		Chunk:    p.Chunk,
	}
}

// Endpoint is one end of a claim: what a document calls the node, and what this
// package's other methods take as an argument.
//
// It is a parameter type rather than four strings on NewClaim because the four
// are two pairs and transposing a pair is silent -- a claim built with the
// subject's ID against the object's name still renders, still cites, and points
// a walk at the wrong node.
type Endpoint struct {
	ID   string
	Name string
}

// String renders the claim as the one line it occupies in a context pack.
//
// The shape is the connector's own — neo4j reports a skipped edge as
// "from -[TYPE]-> to" — so a person reading a pack and a person reading a load
// report are reading the same notation for the same thing.
func (c Claim) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s -[%s]-> %s (%s, %s)", c.From, c.Type, c.To, c.stated(), c.Producer)
	if m := Mark(c.Source, c.Chunk); m != "" {
		b.WriteString(" " + m)
	}
	return b.String()
}

func (c Claim) stated() string {
	if c.Stated {
		return "stated"
	}
	return "inferred"
}

// Mark renders the citation marker a claim carries: [source#chunk].
//
// A chunk below zero prints as [source] alone. alchemy defines -1 as "the
// producer did not work in chunks" — a DDL import, a graph import — so there is
// no chunk to resolve, and printing #-1 would hand an agent a citation it would
// dutifully try to look up and be told does not exist. An empty source prints
// nothing at all rather than an empty bracket, so a claim with no provenance
// reads as a claim with no citation instead of a broken one.
func Mark(source string, chunk int) string {
	if source == "" {
		return ""
	}
	if chunk < 0 {
		return "[" + source + "]"
	}
	return "[" + source + "#" + strconv.Itoa(chunk) + "]"
}

// Citation is the text a claim was extracted from, and where in the file it
// was.
//
// Start and End are byte offsets into the original source, carried through
// from alchemy.Chunk, and they are what make this evidence rather than a
// quotation: a reader holding them can open the file and see the sentence in
// its place, which is the difference between a citation and a claim that a
// citation exists. The old import that caused the load parameter to be a
// parameter had them all at zero, and that is exactly what a reader could not
// tell without asking which import they were looking at.
type Citation struct {
	Source string
	Index  int
	Start  int
	End    int
	Text   string
}

// Contributor is one source that had a hand in a node.
//
// It exists because a comparison of two agent runtimes over one graph produced
// the same wrong answer on both sides, six runs out of six. The graph held
// `Mira` as a single node carrying an edge from a PDF and an edge from a
// hand-written team file: two sources, two mentions of a bare first name,
// joined on exact string equality by the id rule, silently. Meanwhile
// `Nadia` and `Nadia Okonkwo` -- the same corpus, the same kind of
// ambiguity -- were held apart as an Unanswered question, because the strings
// differed.
//
// So the identity risk an agent could see was the one the graph was unsure
// about, and the one it could not see was the one the graph had settled by
// fiat. Every run stated "Mira is the CTO" as though the two mentions were
// established to be one person. None of Find, Claims, Cite or Unanswered could
// have told it otherwise.
//
// This is deliberately not a warning and not a score. It reports what
// contributed and a reader decides: two sources agreeing on a full name and a
// type are corroboration, two agreeing on a first name are a question somebody
// should look at, and a primitive that returned "risky" would be doing the
// judging §2.1 reserves for a person.
type Contributor struct {
	// Source and Chunk locate the mention. Chunk is -1 when the producer did
	// not work in chunks; see Mark.
	Source string
	Chunk  int
	// Producer is what made it, and Stated is alchemy.Producer.Deterministic,
	// computed the way NewClaim computes it and for the same reason.
	Producer alchemy.Producer
	Stated   bool
	// Name is what THIS source called the node, and it is the whole of the
	// question. A node whose contributors all wrote "Nadia Okonkwo" was
	// joined on a full name; one where they wrote "Mira" and "Mira" was joined
	// on a first name. Both are exact matches and they are not the same
	// evidence.
	Name string
}

// Contributions is every source that had a hand in one node.
type Contributions struct {
	// ID and Type are the node asked about, echoed so a caller holding several
	// answers can tell them apart.
	ID   string
	Type string
	// Names are the distinct names the contributors used, sorted. One is the
	// ordinary case; more than one means the store joined records that did not
	// agree about what the thing is called, which a reader must be able to see.
	Names []string
	// Contributors are the mentions, ordered by source then chunk so two reads
	// of one node produce the same document.
	Contributors []Contributor
}

// Joined reports whether more than one source contributed. It is a method
// rather than a field so it cannot fall out of step with the slice.
func (c Contributions) Joined() bool {
	seen := make(map[string]bool, len(c.Contributors))
	for _, x := range c.Contributors {
		seen[x.Source] = true
	}
	return len(seen) > 1
}

// Question is one thing the graph has not decided.
//
// It is the duplicate finding read back, and it is in this interface because
// an agent that cannot ask it answers as though the question had been settled.
// Two nodes that may be one thing are the ordinary case in a corpus imported
// from more than one file, and the finding says only that a signal fired and
// nobody has ruled — which is why neither store makes it an edge between the
// two nodes, and why nothing here is phrased as an assertion.
//
// Left and Right are names and not IDs. Both stores hold the names on the
// finding itself; the IDs are reachable in both but by different routes — an
// edge in one, a column in the other — and a field that one store fills from
// the finding and the other from a second traversal is a field whose cost a
// caller cannot see.
type Question struct {
	// Signal is which check fired. It is alchemy's own type rather than a
	// string for the reason Claim.Producer is: a name-affix match and an
	// identical-attribute match are not equally strong evidence, a reader
	// deciding what to do about the pair has to compare against the names
	// alchemy declares, and a string invites comparing against a literal that
	// no longer exists.
	Signal alchemy.DuplicateSignal
	// Subject is the pair as alchemy rendered it, "left ~ right".
	Subject string
	// Detail states the case in words a person can answer without opening the
	// source.
	Detail      string
	Left, Right string
}

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
