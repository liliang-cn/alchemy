package cortexdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
	cgraph "github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

// Reading a load back out of CortexDB, and why this file did not exist until
// CortexDB grew a way to ask about one.
//
// This is the fourth store to implement recall.Reader and it was the last,
// because it was the only one that could not. The other three hold alchemy's
// graphs and nothing else: a Neo4j database, a pgvector schema and a SPARQL
// endpoint that this connector wrote and this connector scopes. CortexDB is
// somebody's brain. Memories, knowledge, other importers' triples and this
// load all sit in one graph_nodes table, and until v2.89.0 the only filter a
// read had was node_type — which is not a batch. An importer that wrote a
// thousand Person nodes on Tuesday and a thousand more on Friday could ask for
// all two thousand or for none.
//
// So the two honest implementations available were to scan the whole shared
// brain for every question, or to return a Found.Total counted over rows this
// load does not contain. The second is the exact defect Found.Total exists to
// prevent, and the first is a promise that gets slower the more the user
// remembers. Neither was worth shipping, and DESIGN.md recorded the read side
// as deliberately not built.
//
// What changed is upstream: GraphFilter now carries Properties, Contains and
// Limit, ListNodes projects away the vector column, and CountNodes answers what
// a cap cut off. Everything below is scoped through the `run` this connector
// has stamped on every node it writes since it was written — a property that
// was always there and could not be read back.
//
// WHAT THIS STORE ANSWERS WELL AND WHAT IT SCANS. Find and OfType are server
// -side filters: a substring is Contains, a type is an equality on the declared
// type this connector keeps beside CortexDB's own. Types is an aggregation
// CortexDB has no operator for, so it lists the load's entities and counts them
// here — the same trade qdrant's Types names, and cheaper than it, because
// ListNodes leaves the embeddings in the database. Claims, Describe,
// Contributions and Cite are id lookups.
//
// The ordering that recall.Reader promises — by name, then by ID — is done in
// this process rather than in SQL, because the name lives inside a JSON column
// and ordering by it would be a function call per row in the sort. The set
// sorted is one load's answer to one question, never the store.
var _ recall.Reader = (*Loader)(nil)

// finished reports whether a load is present and complete.
//
// Every read below asks, and the question is not "are there nodes with this
// run". completeRun writes a marker document when the load is done, so a load
// that died halfway has entities and no completion — and serving it would be
// the confident wrong answer this whole design is arranged against: a partial
// graph reported as a whole one.
func (l *Loader) finished(ctx context.Context, load string) (bool, error) {
	if load == "" {
		return false, nil
	}
	doc, err := l.cortex.Vector().GetDocument(ctx, completionID(load))
	if err != nil {
		// GetDocument reports an absent document as an error rather than a nil
		// document, and the two are the same answer here. A store that is
		// genuinely broken fails on the read that follows.
		return false, nil
	}
	return doc != nil, nil
}

// noLoad is what the three methods that distinguish an absent load return.
func noLoad(load string) error {
	return fmt.Errorf("%w: %q is not a finished load in this brain; "+
		"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
		recall.ErrNoLoad, load)
}

// scope is the filter every read starts from: one load's entity nodes.
//
// It is the run property and not the id prefix, even though entityNodeID puts
// the run in the id too. The property is what this connector documents as the
// batch marker and what CortexDB's own filter understands; the id shape is an
// internal spelling, and a read that depended on it would break the first time
// either side changed how a node is named.
func (l *Loader) scope(load string, extra ...string) *cgraph.GraphFilter {
	props := map[string]string{l.opts.ReservedPrefix + keyRun: load}
	for i := 0; i+1 < len(extra); i += 2 {
		props[extra[i]] = extra[i+1]
	}
	return &cgraph.GraphFilter{Properties: props}
}

// entities lists one load's entity nodes, and only those.
//
// The prefix check is not belt-and-braces. This connector writes chunk stubs
// and a run marker under the same run, CortexDB writes mention edges between
// them, and a reader that counted a chunk as an entity would report a
// vocabulary the load does not have and a Total nobody could reconcile with
// the graph they were shown.
func (l *Loader) entities(ctx context.Context, f *cgraph.GraphFilter) ([]*cgraph.GraphNode, error) {
	nodes, err := l.cortex.Graph().ListNodes(ctx, f)
	if err != nil {
		return nil, err
	}
	out := nodes[:0]
	for _, n := range nodes {
		if strings.HasPrefix(n.ID, entityPrefix) {
			out = append(out, n)
		}
	}
	return out, nil
}

// entityPrefix is what entityNodeID puts in front of every entity it mints.
const entityPrefix = "entity:alchemy:"

// prop reads a string out of a node's or an edge's property bag.
//
// Everything this connector writes goes in as CortexDB metadata, which is
// map[string]string, so a property that is not a string was written by
// something else — and reading it as one would put another tool's record into
// an alchemy answer.
func prop(p map[string]interface{}, key string) string {
	s, _ := p[key].(string)
	return s
}

// node renders one stored node as the anchor a caller walks from.
//
// The type is the DECLARED type — what alchemy's ontology said — and not
// node_type, which CortexDB may have canonicalised to its own spelling. Both
// are stored precisely so a reader can tell them apart; answering with
// CortexDB's would report a vocabulary that no ontology declares, and Types
// and OfType would then disagree with the ontology the load was checked
// against.
func (l *Loader) node(n *cgraph.GraphNode) recall.Node {
	pre := l.opts.ReservedPrefix
	return recall.Node{
		ID:   prop(n.Properties, pre+keyEntityID),
		Type: prop(n.Properties, pre+keyDeclaredType),
		Name: prop(n.Properties, "name"),
	}
}

// page sorts and cuts, and is the one place the interface's ordering promise
// is kept for this store.
//
// By name and then by ID, so that a limit cuts the same place twice: two names
// that are equal are ordered by something that cannot be, and without the
// second key a page would shuffle between two identical calls.
func page(hits []recall.Node, limit int) recall.Found {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Name != hits[j].Name {
			return hits[i].Name < hits[j].Name
		}
		return hits[i].ID < hits[j].ID
	})
	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if hits == nil {
		hits = []recall.Node{}
	}
	return recall.Found{Nodes: hits, Total: total}
}

// Find returns the entities of one load whose name contains name.
//
// The substring match is CortexDB's, through GraphFilter.Contains, which folds
// case on both sides and escapes the needle. That escaping is load-bearing for
// this caller in particular: alchemy imports schemas, and a search for a column
// called user_id would otherwise also match userxid, because LIKE reads an
// underscore as a wildcard.
//
// An empty name matches everything, which is what a substring search for the
// empty string means everywhere else in this interface. It is not a sentinel:
// Contains skips an empty value rather than filtering on it, so the answer is
// the load's entities, ordered and paged like any other.
func (l *Loader) Find(ctx context.Context, load, name string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("cortexdb: limit = %d is not a number of anchors", limit)
	}
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return recall.Found{Nodes: []recall.Node{}}, err
	}
	f := l.scope(load)
	f.Contains = map[string]string{"name": name}
	nodes, err := l.entities(ctx, f)
	if err != nil {
		return recall.Found{}, fmt.Errorf("cortexdb: find %q in load %q: %w", name, load, err)
	}
	hits := make([]recall.Node, 0, len(nodes))
	for _, n := range nodes {
		hits = append(hits, l.node(n))
	}
	return page(hits, limit), nil
}

// Types is the vocabulary of one load: every entity type in it and how many
// entities carry it.
//
// Counted here rather than asked of the store, because CortexDB has no GROUP
// BY this package can reach. The scan is the load's entity nodes without their
// vectors — which is what ListNodes exists for, and the difference between
// counting names and moving the embeddings that sit beside them.
//
// The count is of entities and not of nodes. Chunk stubs and the run marker
// share the run, so counting nodes would report types nobody declared and
// totals that do not add up to the graph a reader was shown.
func (l *Loader) Types(ctx context.Context, load string) ([]recall.TypeCount, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return nil, err
	}
	nodes, err := l.entities(ctx, l.scope(load))
	if err != nil {
		return nil, fmt.Errorf("cortexdb: types of load %q: %w", load, err)
	}
	counts := map[string]int{}
	for _, n := range nodes {
		counts[prop(n.Properties, l.opts.ReservedPrefix+keyDeclaredType)]++
	}
	out := make([]recall.TypeCount, 0, len(counts))
	for t, c := range counts {
		out = append(out, recall.TypeCount{Type: t, Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// OfType returns the entities of one load of exactly this type.
//
// Exactly, and this store makes that easy: the declared type is its own
// property, so the match is an equality the database resolves rather than the
// case-insensitive substring Find uses. The difference is deliberate — Find
// takes what somebody typed, this takes what Types returned — and getting it
// wrong here would report "Person" and "person" as one class in a store where
// an ontology says they are two.
func (l *Loader) OfType(ctx context.Context, load, typ string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("cortexdb: limit = %d is not a number of entities", limit)
	}
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return recall.Found{Nodes: []recall.Node{}}, err
	}
	nodes, err := l.entities(ctx, l.scope(load, l.opts.ReservedPrefix+keyDeclaredType, typ))
	if err != nil {
		return recall.Found{}, fmt.Errorf("cortexdb: entities of type %q in load %q: %w", typ, load, err)
	}
	hits := make([]recall.Node, 0, len(nodes))
	for _, n := range nodes {
		hits = append(hits, l.node(n))
	}
	return page(hits, limit), nil
}

// provenanceFrom reads back what provenanceMeta wrote.
//
// It is the inverse of that function and has to stay one, which is why both
// live under the same key constants rather than under two lists of strings.
// Chunk defaults to -1 and not to 0: alchemy defines -1 as "the producer did
// not work in chunks", and 0 is a legal chunk index — a record whose chunk
// failed to parse would otherwise cite the first chunk of its file, with
// nothing about the answer looking wrong.
func provenanceFrom(meta map[string]interface{}, pre string) alchemy.Provenance {
	p := alchemy.Provenance{
		Source:     prop(meta, pre+keySource),
		Chunk:      -1,
		Producer:   alchemy.Producer(prop(meta, pre+keyProducer)),
		Model:      prop(meta, pre+keyModel),
		Ontology:   prop(meta, pre+keyOntology),
		Chunking:   prop(meta, pre+keyChunking),
		ReviewedBy: prop(meta, pre+keyReviewedBy),
		RuleSet:    prop(meta, pre+keyRuleSet),
		RuledBy:    prop(meta, pre+keyRuledBy),
		By:         prop(meta, pre+keyBy),
		At:         prop(meta, pre+keyAt),
	}
	if n, err := strconv.Atoi(prop(meta, pre+keyChunk)); err == nil {
		p.Chunk = n
	}
	if c, err := strconv.ParseFloat(prop(meta, pre+keyConfidence), 64); err == nil {
		p.Confidence = c
	}
	return p
}

// provenancesOf reads an edge's whole provenance list.
//
// writeRelations stores every member's provenance as a JSON array under the
// reserved `provenance` key, because CortexDB's identity rule can make one edge
// out of several alchemy relations and only one of them fits the flat fields.
// Reading the array back is what makes a fused edge answer as the several
// claims it is, rather than as the one the store happens to hold.
//
// The flat fields are the fallback and not the source: an edge written before
// the array existed, or by something else entirely, still answers with one.
func provenancesOf(props map[string]interface{}, pre string) []alchemy.Provenance {
	if raw := prop(props, pre+keyProvenance); raw != "" {
		var out []alchemy.Provenance
		if err := json.Unmarshal([]byte(raw), &out); err == nil && len(out) > 0 {
			return out
		}
	}
	return []alchemy.Provenance{provenanceFrom(props, pre)}
}
