// Package graphimport reads a knowledge-graph JSON document that another tool
// already produced and turns it into entities, relations and chunks.
//
// Nothing here is inferred about the world: a graph already asserted all of
// it, so every entity and relation is produced deterministically and carries
// Producer graph-import with no model and no confidence (DESIGN.md §5b).
// Node summaries become chunks for the embedder stage to vectorise later.
//
// What is inferred is the *shape of the document*, and that is the whole of
// the work. There is no standard knowledge-graph JSON; every tool spells the
// same six things differently, so this package accepts the spellings real
// tools emit and refuses to choose when a document uses two of them at once.
// The rule throughout: a spelling missing or blank states nothing, two
// spellings that agree state one thing twice, and two that disagree are a
// question only a person can answer — so the document is refused rather than
// read under a coin flip. See AmbiguityError.
//
// A document is refused whole rather than partially imported when the
// question is about identity, because a half-read graph looks complete. A
// broken edge is different: it is reported as a Violation and kept, because
// the rest of the graph is usable without it (§7.3).
package graphimport

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// ChunkStrategy names what produced these chunks. It is not a splitter's
// name because no splitting happened: a chunk here is one node's summary,
// whole, exactly as the upstream graph wrote it. §7.1 says a graph
// re-extracted under a different strategy is a different graph, so the field
// has to say which — and "graph-node-summary" tells a reader that the
// boundaries were decided upstream by whoever wrote the node, not here by a
// window size they could tune.
const ChunkStrategy = "graph-node-summary"

// NoSpan is what Chunk.Start and Chunk.End carry for every chunk this package
// emits. A node summary is a JSON string member, not a span of prose, so
// there is no offset into the source that would find it; 0 and 0 would be an
// accidental zero that reads as "the start of the file", and 0..len(text)
// would point at the wrong document entirely. -1 is the convention this
// contract already uses for "the producer does not work that way", the same
// value Provenance.Chunk carries here.
const NoSpan = -1

// directionForward is the only value the "direction" member is understood to
// take: the edge runs from the endpoint it wrote as the source to the one it
// wrote as the target, which is what every reader here already assumed. It is
// the only value any document read while writing this package contains — all
// 21854 edges of the real code graph behind pkg/verify/testdata say it — and
// see DirectionError for why an unobserved value is refused rather than given a
// meaning it was never seen to have.
const directionForward = "forward"

// Result is what one document yielded.
type Result struct {
	Entities   []alchemy.Entity
	Relations  []alchemy.Relation
	Chunks     []alchemy.Chunk
	Violations []alchemy.Violation
	// Guesses are the identity inferences this package made. §2.1: a guess
	// that does not announce itself is a bug with a three-month fuse.
	Guesses []alchemy.Guess
}

// Counts is the block DESIGN.md §5 makes an obligation: every returned graph
// is accompanied by the numbers needed to distrust it.
//
// Inferred is 0 and Deterministic is every relation, because a graph import
// asserts nothing of its own — the document already said all of it. Stating
// the zero rather than leaving the field out is the point: a caller merging
// this result with an llm-extract one can add the columns up and see which
// half of the graph was guessed.
//
// ChunksEmpty stays 0. It means an extraction that produced nothing, and a
// node that states no summary is not a failed extraction — it is a node with
// nothing to vectorise, which is normal and carries no signal.
func (r Result) Counts() alchemy.Counts {
	return alchemy.Counts{
		Entities:      len(r.Entities),
		Relations:     len(r.Relations),
		Deterministic: len(r.Relations),
		Violations:    len(r.Violations),
		Guesses:       len(r.Guesses),
	}
}

// The spellings this package accepts, and why each one is on the list. Every
// spelling here was found in a document some real tool writes today; none is
// speculative, because a tolerated spelling nobody emits can only ever
// misread a document that meant something else by it.
//
// The two formats read while writing this were Understand-Anything's
// knowledge-graph.json (what oss-agent's own graph importer consumes:
// {name, nodes, edges, layers, tour}, nodes as id/type/name/filePath/summary,
// edges as source/target/type/direction/weight) and CortexDB's three graph
// shapes (graphflow's id/label/type/summary and source/target/relation,
// liveview's id/label/type and source/target/label, and a side graph's
// name/type/note and from/to/type). The RDF spellings come from CortexDB's
// knowledge_graph_export, which emits triples: a JSON rendering of those is
// subject/predicate/object.
//
// Edges:
//
//	from  — "source" (Understand-Anything, CortexDB graphflow and liveview),
//	        "from" (CortexDB side graphs), "subject" (triples).
//	to    — "target", "to", "object", from those same three.
//	type  — "type" (Understand-Anything, side graphs), "relation"
//	        (CortexDB graphflow), "predicate" (triples), "label"
//	        (CortexDB liveview, whose edges carry no name — the label *is*
//	        the relation).
//
// Nodes:
//
//	id      — "id". Only one spelling is accepted, because an id is what
//	          edges resolve against and a document that calls it something
//	          else is a document whose edges would not resolve anyway. A node
//	          with no id falls back to its name, reported as a Guess.
//	type    — "type".
//	name    — "name" (Understand-Anything, side graphs), "label" (CortexDB
//	          graphflow and liveview).
//	summary — "summary" (both formats above), "description".
//
// On a node "label" means the opposite of what it means on an edge: it is the
// display name, because liveview and graphflow both write id+label+type and
// keep the type in "type". The asymmetry is real and belongs to the formats,
// not to us — which is why the two lists are separate rather than one shared
// set of aliases.
//
// The collections themselves are spelled two ways as well: nodes/entities and
// edges/relations/links.
var (
	// edgeID is the edge's own name for itself, and only one spelling is
	// accepted for the same reason nodeID accepts one: an id a document spells
	// its own way is an id nothing else in the document refers to.
	//
	// It is read into alchemy.Relation.Key rather than left among the
	// attributes because it is not something the edge says about the world, it
	// is which edge this is. A document that states two call sites from one
	// function to another writes two edges with two ids, and a verifier that
	// saw only from/to/type would read them as two sources disagreeing about
	// one edge's id — the same false conflict a schema's two foreign keys onto
	// one table used to raise. Most graph documents state no edge ids at all,
	// and those are unchanged: no key, and identity stays what it was.
	edgeID   = []string{"id"}
	edgeFrom = []string{"source", "from", "subject"}
	edgeTo   = []string{"target", "to", "object"}
	edgeType = []string{"type", "relation", "predicate", "label"}
	// edgeDirection is which way the record runs relative to the endpoints it
	// just wrote down. Understand-Anything states it on every edge; nothing
	// else read while writing this package states it at all.
	//
	// It is read rather than left among the attributes because it is not
	// something the edge says about the world — it is how to read the edge, in
	// the same family as from and to. The only value ever observed is
	// "forward", which states what this package already assumed; see
	// DirectionError for why any other value refuses the document instead of
	// being carried along as an attribute nobody consults.
	//
	// What it does NOT say is anything about the relation *type*. Both halves
	// of a mutual pair are written "forward" — both files really do import each
	// other — so a producer stating it has stated two facts, not declared that
	// `imports` may run both ways. That declaration belongs to an ontology
	// (pkg/ontology's RelationType.BothWays); §5 keeps the ontology an input,
	// and inferring one from the shape of the data is the automatic ontology
	// generation it rules out.
	edgeDirection = []string{"direction"}
	nodeID        = []string{"id"}
	nodeType      = []string{"type"}
	nodeName      = []string{"name", "label"}
	nodeSummary   = []string{"summary", "description"}
)

// document holds every accepted spelling of the two collections rather than
// decoding into a map, so that encoding/json's byte offsets still point into
// the whole file when a member turns out to be the wrong shape.
//
// Members this package does not name are ignored on purpose. A
// knowledge-graph.json also carries "layers" and "tour", which are containers
// over nodes rather than nodes: importing them would mean inventing entity
// types the document never declared for the graph they were grouping.
// The pointers distinguish "this document says it has no nodes" from "this
// document never mentions nodes"; see Parse.
type document struct {
	Nodes    *[]object `json:"nodes"`
	Entities *[]object `json:"entities"`

	Edges     *[]object `json:"edges"`
	Relations *[]object `json:"relations"`
	Links     *[]object `json:"links"`
}

// deref turns the optional lists into what collection compares, keeping the
// spellings the document actually used.
func deref(lists ...*[]object) (out [][]object, present bool) {
	out = make([][]object, len(lists))
	for i, l := range lists {
		if l == nil {
			continue
		}
		present = true
		out[i] = *l
	}
	return out, present
}

// collection chooses between the spellings of one list. An empty list is not
// a competing claim — a writer that emits every key it knows says nothing by
// writing "edges": [] — and two spellings holding the same list agree. Two
// that hold different lists are the same undecidable question an edge with
// two endpoints poses, one scale up.
func collection(slot string, spellings []string, lists [][]object) ([]object, error) {
	var chosen []object
	var got, values []string
	for i, list := range lists {
		if len(list) == 0 {
			continue
		}
		if chosen == nil {
			chosen = list
		}
		got = append(got, spellings[i])
		values = append(values, fmt.Sprintf("%d entries", len(list)))
	}
	for _, list := range lists {
		if len(list) > 0 && !reflect.DeepEqual(list, chosen) {
			return nil, &AmbiguityError{Location: "document", Slot: slot, Spellings: got, Values: values}
		}
	}
	return chosen, nil
}

// object is a node or edge kept as raw members, because which member holds
// the id cannot be decided by a struct tag: it depends on the document.
type object map[string]json.RawMessage

// Parse reads the whole document and returns what it asserted.
func Parse(source string, r io.Reader) (Result, error) {
	var res Result
	b, err := io.ReadAll(r)
	if err != nil {
		// A reader that failed is not a document that is wrong, and calling
		// it malformed would send someone to inspect a file that is fine.
		return res, fmt.Errorf("read %s: %w", source, err)
	}
	var doc document
	if err := json.Unmarshal(b, &doc); err != nil {
		return res, malformed(source, b, err)
	}
	nodeLists, haveNodes := deref(doc.Nodes, doc.Entities)
	edgeLists, haveEdges := deref(doc.Edges, doc.Relations, doc.Links)
	// A document naming neither collection is not an empty graph, it is some
	// other kind of document — CortexDB's knowledge_graph_export writes
	// {"format", "content"}, and an RDF payload read as a graph would come
	// back as a confident nothing. Returning empty here would make "this file
	// is not a graph" indistinguishable from "this graph has no nodes", which
	// is the §2.1 failure with an import job attached. Stating "nodes": [] is
	// a different thing and is accepted.
	if !haveNodes && !haveEdges {
		return res, fmt.Errorf("%s: no node or edge list; expected one of nodes/entities and one of edges/relations/links", source)
	}
	nodes, err := collection("nodes", []string{"nodes", "entities"}, nodeLists)
	if err != nil {
		return res, err
	}
	edges, err := collection("edges", []string{"edges", "relations", "links"}, edgeLists)
	if err != nil {
		return res, err
	}

	// seen remembers what each id was first said to be, so a repeat can be
	// told from a contradiction.
	type stated struct {
		pos     int
		entity  alchemy.Entity
		summary string
	}
	seen := make(map[string]stated, len(nodes))
	for i, n := range nodes {
		where := fmt.Sprintf("node %d", i)
		ent, summary, guess, err := n.entity(where, source)
		if err != nil {
			return Result{}, err
		}
		// A node whose id was already used is either a redundant repeat or a
		// contradiction; only the second is a corruption. See
		// DuplicateNodeError for why the contradiction is refused rather than
		// resolved by document order.
		if first, ok := seen[ent.ID]; ok {
			if !reflect.DeepEqual(first.entity, ent) || first.summary != summary {
				return Result{}, &DuplicateNodeError{ID: ent.ID, First: first.pos, Second: i}
			}
			continue
		}
		seen[ent.ID] = stated{pos: i, entity: ent, summary: summary}
		if guess != nil {
			res.Guesses = append(res.Guesses, *guess)
		}
		res.Entities = append(res.Entities, ent)
		if summary != "" {
			res.Chunks = append(res.Chunks, alchemy.Chunk{
				Index:    len(res.Chunks),
				Text:     summary,
				Source:   source,
				Strategy: ChunkStrategy,
				Heading:  ent.Name,
				Start:    NoSpan,
				End:      NoSpan,
			})
		}
	}

	known := make(map[string]bool, len(res.Entities))
	for _, e := range res.Entities {
		known[e.ID] = true
	}
	for i, e := range edges {
		where := fmt.Sprintf("edge %d", i)
		from, err := e.pick(where, "from", edgeFrom)
		if err != nil {
			return Result{}, err
		}
		to, err := e.pick(where, "to", edgeTo)
		if err != nil {
			return Result{}, err
		}
		typ, err := e.pick(where, "type", edgeType)
		if err != nil {
			return Result{}, err
		}
		key, err := e.pick(where, "id", edgeID)
		if err != nil {
			return Result{}, err
		}
		// Checked before the missing-slot loop below, because a direction
		// nobody can read makes the endpoints unreadable too: which of them is
		// "from" is exactly what the value would have decided.
		dir, err := e.pick(where, "direction", edgeDirection)
		if err != nil {
			return Result{}, err
		}
		if dir != "" && !strings.EqualFold(dir, directionForward) {
			return Result{}, &DirectionError{Location: where, Value: dir}
		}
		// An edge missing an endpoint or a type is refused rather than
		// reported. A dangling edge points at something and is kept because
		// the claim survives its broken half (§7.3); an edge with no source
		// makes no claim, and admitting it would put a relation with an empty
		// From — a node id nothing can ever match — into the graph.
		// Checked in a fixed order: an edge missing two slots must name the
		// same one every run, or the error message stops being reproducible.
		for _, slot := range []struct{ name, value string }{{"from", from}, {"to", to}, {"type", typ}} {
			if slot.value == "" {
				return Result{}, fmt.Errorf("%s: states no %s, so it asserts nothing that could be placed in the graph", where, slot.name)
			}
		}
		res.Relations = append(res.Relations, alchemy.Relation{
			From: from, To: to, Type: typ, Key: key, Provenance: prov(source),
			Attributes: e.rest(edgeID, edgeFrom, edgeTo, edgeType, edgeDirection),
		})
		// An edge naming a node the document does not contain is reported and
		// kept. Dropping it would leave a graph that looks complete and is
		// quietly smaller than what the document said; §7.3 wants the failure
		// attributable and excludable instead, and a caller that wants the
		// clean subgraph can exclude the subjects named here.
		var missing []string
		if !known[from] {
			missing = append(missing, from)
		}
		if to != from && !known[to] {
			missing = append(missing, to)
		}
		if len(missing) > 0 {
			res.Violations = append(res.Violations, alchemy.Violation{
				Kind: alchemy.ViolationDanglingRelation,
				Detail: fmt.Sprintf("%s names %d node(s) this document does not contain: %s",
					where, len(missing), strings.Join(quoteAll(missing), ", ")),
				Subject:    fmt.Sprintf("%s -[%s]-> %s", from, typ, to),
				Provenance: prov(source),
			})
		}
	}
	return res, nil
}

// entity reads one node object. It returns the entity, the summary that
// should become a chunk (empty when the node states none), and the identity
// guess it had to make, if any.
func (o object) entity(where, source string) (alchemy.Entity, string, *alchemy.Guess, error) {
	id, err := o.pick(where, "id", nodeID)
	if err != nil {
		return alchemy.Entity{}, "", nil, err
	}
	typ, err := o.pick(where, "type", nodeType)
	if err != nil {
		return alchemy.Entity{}, "", nil, err
	}
	name, err := o.pick(where, "name", nodeName)
	if err != nil {
		return alchemy.Entity{}, "", nil, err
	}
	summary, err := o.pick(where, "summary", nodeSummary)
	if err != nil {
		return alchemy.Entity{}, "", nil, err
	}

	// A node with no id is identified by its name. CortexDB side graphs write
	// nodes that way and their edges refer to nodes by name, so this is the
	// only reading under which such a document resolves — but it is still an
	// inference about identity, and two nodes that happen to share a name
	// become one node under it. So it is reported. Alternatives stays empty
	// because nothing was ranked: there was one candidate, and the guess is
	// that a name may serve as an identity at all, not which name won.
	var guess *alchemy.Guess
	if id == "" {
		if name == "" {
			return alchemy.Entity{}, "", nil, fmt.Errorf("%s: has neither an id nor a name, so nothing can refer to it", where)
		}
		id = name
		guess = &alchemy.Guess{
			Field:      where + " id",
			ChosenAs:   id,
			Reason:     "the node states no id, so its name is used as the identity edges resolve against",
			Provenance: prov(source),
		}
	}
	return alchemy.Entity{
		ID: id, Type: typ, Name: name, Provenance: prov(source),
		Attributes: o.rest(nodeID, nodeType, nodeName, nodeSummary),
	}, summary, guess, nil
}

// pick returns the value a slot was given, or an AmbiguityError when two
// accepted spellings of it disagree. An empty string is not a statement, so a
// member present but blank is skipped rather than counted as a claim.
func (o object) pick(where, slot string, spellings []string) (string, error) {
	var chosen string
	var got, values []string
	for _, s := range spellings {
		v := o.str(s)
		if v == "" {
			continue
		}
		if chosen == "" {
			chosen = v
		}
		got = append(got, s)
		values = append(values, v)
	}
	for _, v := range values {
		if v != chosen {
			return "", &AmbiguityError{Location: where, Slot: slot, Spellings: got, Values: values}
		}
	}
	return chosen, nil
}

// rest is everything the document stated that no slot claimed, kept under the
// spelling it used. Entity.Attributes is defined as whatever the source
// stated beyond type and name, and a graph import that dropped filePath,
// tags or an edge's weight would be deciding on the reader's behalf which of
// the upstream tool's assertions were worth keeping.
//
// Every accepted spelling of a claimed slot is excluded, including ones this
// object left blank: a member named "from" is a statement about the edge's
// endpoint whatever it holds, and re-exporting it as an attribute would put
// two spellings of the same thing in one object.
func (o object) rest(claimed ...[]string) map[string]any {
	skip := make(map[string]bool)
	for _, group := range claimed {
		for _, s := range group {
			skip[s] = true
		}
	}
	var out map[string]any
	for k, raw := range o {
		if skip[k] {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		if out == nil {
			out = make(map[string]any)
		}
		out[k] = v
	}
	return out
}

// str reads one member as a string, ignoring members that are not strings —
// a document that puts an object where an id belongs is saying nothing this
// package can use, and guessing a rendering of it would invent an id.
func (o object) str(key string) string {
	raw, ok := o[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// prov is the only provenance this package ever produces. Chunk is -1 because
// a graph import does not work in chunks, and Model/Confidence stay zero
// because nothing here was inferred: the document already asserted all of it
// (DESIGN.md §5b).
func prov(source string) alchemy.Provenance {
	return alchemy.Provenance{Source: source, Chunk: -1, Producer: alchemy.ProducerGraphImport}
}
