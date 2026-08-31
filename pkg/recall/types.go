// The values recall.Reader answers in.
//
// They are here rather than beside the interface because the interface is one
// argument -- that a read is scoped to one load -- and these are eight
// separate ones about what a caller needs in order to weigh what they are
// handed. Each type carries the measurement that gave it its shape, and those
// measurements are what makes the file long; splitting them out keeps the
// interface readable as the list of questions it is.
package recall

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

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
	// By and At are the person who asserted this and when, empty for the
	// producers that have nobody behind them.
	//
	// They are here because a claim with no date reads as timeless, and some
	// claims are not. Measured: an absence asserted by a named colleague in
	// August was still being reported as current the following March -- the
	// store held `_by` and `_at`, every connector wrote them, and no reader
	// could see them, so the agent had no way to notice the claim was seven
	// months old. "human" already tells a reader somebody can be asked; these
	// two say who, and how long ago they said it.
	By string
	At string
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
		By:       p.By,
		At:       p.At,
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
	fmt.Fprintf(&b, "%s -[%s]-> %s (%s, %s", c.From, c.Type, c.To, c.stated(), c.Producer)
	// Only when there is one. An extraction has nobody behind it and no date
	// that means anything to a reader, and printing an empty clause on every
	// line would train a reader to skip the clause on the lines that have one.
	if c.At != "" {
		fmt.Fprintf(&b, ", asserted %s", c.At)
	}
	if c.By != "" {
		fmt.Fprintf(&b, " by %s", c.By)
	}
	b.WriteString(")")
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

// TypeCount is one entity type in a load and how many entities carry it.
//
// The count is here rather than left to a second call because it is what makes
// the answer usable. A caller told a load holds Person and Product knows what
// kinds of question it can ask; a caller told it holds twenty People and nine
// Products knows what limit to pass to OfType, which is the difference between
// enumerating a type and truncating it.
type TypeCount struct {
	Type  string
	Count int
}

// Description is one entity as the store holds it: what it is, what it is
// called, everything the record said about it, and where the record came from.
//
// It exists because pkg/recall could not read an entity. It could find one by
// name, list one by type, walk its edges, cite the text behind a claim, ask
// what was unanswered and ask who contributed -- and none of those returns the
// node itself. Attributes and aliases were write-only: every store keeps them,
// and no reader could get them back.
//
// Measured, and the shape of the failure is why this is a separate call rather
// than more fields on Node. An absence recorded the best way the model allows
// -- an entity carrying `from`, `to`, `start_confirmed` and the announcement
// verbatim, asserted by a named person on a dated message -- was asked about
// four months after it ended. The agent could see there was a time-bounded fact
// and went looking for the bounds: it re-read the node three times, tried twice
// to cite the announcement, and every answer came back as the same dateless
// sentence. It then answered from the node's NAME, and dropped a developer from
// a contact list over a leave sixteen months in the past. Nineteen tool calls,
// six stored fields, none of them reachable.
//
// It is not Node with more on it. Node is what an anchor search returns twenty
// of, and twenty of these is a page of JSON where a person wanted a list of
// candidates; that argument is why Node is three fields and it is still right.
// Find returns candidates and this returns one thing in full, which is the
// division an agent already works in.
type Description struct {
	ID   string
	Type string
	Name string
	// Aliases are the other names a source gave this thing. skos:altLabel and
	// never owl:sameAs: an alias is what a source said, not a resemblance a
	// machine guessed.
	Aliases []string
	// Attributes are the record's own fields, in the JSON value domain
	// alchemy.Entity.Attributes uses. They are returned whole rather than
	// filtered, because this package has no opinion about which of a buyer's
	// fields matter and a reader asking about one entity has already narrowed.
	Attributes map[string]any
	// Provenance is the WHOLE of it, not the four fields Claim carries. This is
	// the one call that returns a record rather than a sentence, so it is the
	// place the model, the confidence, the ontology, the reviewer and the
	// asserter with their date are reachable at all.
	Provenance alchemy.Provenance
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
