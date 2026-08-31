// Package rdf loads an alchemy.Result into an RDF triple store, and reads one
// back. It is written against Ontotext GraphDB and speaks only the
// RDF4J-compatible HTTP protocol, so nothing in it is GraphDB's except the
// repository-creation route the live tests use.
//
// # The problem this package exists to solve
//
// An RDF triple is three terms and nothing else. It cannot carry a property,
// and every alchemy relation carries a provenance — the source, the chunk, the
// producer, the model, the confidence, the reviewer, the rule set, the person
// and the date (DESIGN.md §5b). So the central question for this connector is
// not how to write a graph, which RDF is made for; it is where the eleven
// fields hanging off each edge are supposed to go.
//
// There were four candidates and they were measured against a live store rather
// than argued about:
//
//   - RDF reification: state the triple again as three statements about a
//     resource, then hang the provenance off that resource. It costs four
//     triples per edge before any provenance, the edge is asserted twice under
//     two identities, and — the part that decides it — reading the edge and its
//     provenance together is a join over rdf:subject, rdf:predicate and
//     rdf:object, which no store indexes as a unit.
//   - A singleton property per assertion: mint a fresh predicate p1 for this
//     one edge, say p1 rdf:singletonPropertyOf USES, hang the provenance off
//     p1. Every edge becomes its own predicate, so a store's predicate index —
//     the one index every triple store is built around — has one entry per
//     edge, and "every USES edge" stops being a lookup.
//   - A named graph per source: put each source's statements in their own graph
//     and describe the graph. It is the standard answer and it fails on the
//     shape of the data rather than on principle: provenance here is per
//     record, not per file, so two edges from two chunks of one PDF have
//     different provenance and would need different graphs — and named graphs
//     are already spoken for by the load (see below).
//   - RDF-star: name the triple itself, << s p o >>, and hang the provenance
//     off that.
//
// RDF-star works natively on GraphDB, in both directions, in one query.
// Measured, not assumed: the write is a 204, and
//
//	SELECT ?s ?p ?o ?producer ?src ?chunk WHERE {
//	  ?s ?p ?o . << ?s ?p ?o >> al:producer ?producer ; al:source ?src ; al:chunk ?chunk . }
//
// returns the edge and its provenance together in one row. So that is what this
// package writes: the edge is an ordinary asserted triple that every RDF tool
// in the world can read, and its provenance is annotated onto the quoted form
// of that same triple. No reification, no singleton properties, no
// graph-per-source.
//
// The same shape carries an entity's provenance, annotated onto the entity's
// own rdf:type statement — the assertion that brought it into the graph. One
// shape for both is §5b's guarantee kept in one query language rather than two:
// an entity and a relation can both name their producer, and a reader asks the
// same way.
//
// # What RDF-star costs, stated rather than hidden
//
// RDF is a set of triples, so two assertions of one edge are one triple. Where
// a property graph keeps two parallel edges with two provenances — which is
// exactly what neo4j's relationKey is for — this store cannot, and annotating
// the one quoted triple twice would put two sources and two producers on it
// with nothing saying which goes with which. So the first assertion of an edge
// is written and a second one that differs is reported, in Report.Lost and in
// Report.MergedRelations, rather than being folded in. See tx.go.
//
// # Named graphs, which are for loads and nothing else
//
// Each load goes in its own named graph, named by the load. That is orthogonal
// to the provenance question above and it is the one place named graphs are the
// right tool: sink.Ident already makes a load the unit of identity, Replace
// becomes DROP GRAPH, and two loads of the same corpus cannot merge into one
// answer.
//
// Entity IRIs are load-scoped as well, and the graph alone would not have been
// enough — an IRI is global, so the same IRI in two graphs is the same
// resource, and any query over the union would fuse two unrelated things that
// happened to share an alchemy.Entity.ID. Vocabulary IRIs are deliberately not
// load-scoped; see names.go.
//
// # The mapping
//
//	entity type          rdfs:Class, and the entity rdf:type's it
//	relation type        an IRI predicate
//	Entity.Name          rdfs:label
//	Entity.Aliases       skos:altLabel
//	ontology description rdfs:comment
//	at_most_one_out/in   owl:FunctionalProperty / owl:InverseFunctionalProperty
//	duplicate finding    skos:closeMatch, and never owl:sameAs
//
// The last row is load-bearing. A duplicate finding is a QUESTION nobody has
// answered — alchemy explicitly refuses to act on it — and owl:sameAs asserts
// identity, so a reasoner given one would merge two nodes on evidence the
// producer declined to merge them on. skos:closeMatch is the weaker term that
// licenses nothing: it says the two may be interchangeable for some purposes,
// which is what a finding says. And the one-hop walk excludes it anyway, so an
// agent is never handed the guess as a claim.
//
// The ontology's `from` and `to` are NOT rdfs:domain and rdfs:range, and
// ontology.go carries that argument in full: alchemy's ends are constraints and
// RDFS's are inference licences, which is the same syntax with the opposite
// meaning.
package rdf

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Loader writes results into one repository under one set of options, and
// reads them back.
type Loader struct {
	// report is where a load in progress accumulates what it wrote. It is set
	// by Load on a per-load copy and is nil on the one a caller holds; see
	// Load for why these numbers are not the envelope's.
	report *Report
	opts   Options
}

// Open returns a Loader and checks that the repository answers before handing
// it back. A constructor that failed lazily would make the first error a caller
// sees arrive in the middle of a load, attributed to their data.
func Open(ctx context.Context, o Options) (*Loader, error) {
	l := New(o)
	if l.opts.Endpoint == "" {
		return nil, fmt.Errorf("rdf: Options.Endpoint is required")
	}
	if l.opts.Repository == "" {
		return nil, fmt.Errorf("rdf: Options.Repository is required; a connector that invented one " +
			"would write a customer's graph into a repository nobody knows to back up")
	}
	if err := l.Ping(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// New builds a Loader without touching the network. It is what the query
// builders are tested through: a SPARQL statement is the kind of code that is
// otherwise only ever exercised on the machines that have a server.
func New(o Options) *Loader { return &Loader{opts: o.withDefaults()} }

// Report says what a Load did. It is returned rather than logged because
// everything in it is a fact about the graph the caller now has.
type Report struct {
	Load   string
	Digest string
	// Graph is the named graph this load was written into, which is the one
	// string a buyer needs in order to query it, drop it, or export it.
	Graph string

	Entities   int
	Relations  int
	Chunks     int
	Violations int
	Duplicates int
	Guesses    int
	Unread     int
	// Supersessions is how many retirements were filed beside the graph. It is
	// a count of claims recorded and never of records removed.
	Supersessions int
	RuleSets      int

	// SkippedRelations names the edges that were not written because an
	// endpoint was not in the result.
	SkippedRelations []string
	// MergedRelations names the assertions that could not be kept apart,
	// because RDF is a set of triples and they assert an edge another record
	// in this result already asserted. See tx.go: this is what RDF-star costs
	// where a property graph pays nothing.
	MergedRelations []string
	// SkippedVectors is how many embeddings were left behind. A triple store
	// holds no embeddings, and dropping them without saying so would be the
	// silent loss the rest of this design refuses.
	SkippedVectors int

	// Requests is how many HTTP round trips it took, which is the number an
	// operator needs when a load dies halfway. It is the same number
	// sink.Report calls Batches.
	Requests int
	// Replay is true when the load was already present with the same digest.
	Replay bool
}

// Load writes a whole result into the store.
//
// The sequence is the one neo4j's Load argues for and it survives the change of
// store unchanged, because the reason is §8.4's and not Cypher's: a large
// result does not fit in one request, so a load is many requests, so a load can
// fail with part of the graph written.
//
//  1. Everything checkable without a server is checked first, and the load is
//     refused before a single write if anything is wrong.
//  2. The load marker is written into the load's graph, saying al:complete
//     false. From that instant until the last batch lands, the store
//     truthfully says the load is mid-import, and every read in recall.go
//     requires the marker to say true.
//  3. The batches run.
//  4. The marker is flipped, with the digest and the counts.
//
// A crash at any point leaves a graph whose marker says false, which is one
// query to find and one re-Load to finish. RDF makes that cheaper than it is
// anywhere else: a graph is a set, so re-writing a statement that is already
// there is genuinely a no-op rather than an idempotent update that has to be
// arranged.
func (l *Loader) Load(ctx context.Context, res alchemy.Result) (Report, error) {
	// This connector's own refusals first, so that a caller matching on
	// ErrHeld or ErrNoRunID keeps matching on them. §4.1 moved the *shared*
	// refusals above the line and not this store's account of them.
	opts, err := preflight(res, l.opts)
	if err != nil {
		return Report{}, err
	}

	out := Report{Load: opts.RunID}
	run := *l
	run.opts = opts
	run.report = &out
	out.Graph = run.loadIRI(opts.RunID)

	rep, err := sink.Load(ctx, &run, res, sink.Options{
		Load: opts.RunID, Replace: l.opts.Overwrite, Batch: l.opts.BatchSize,
	})
	out.Digest = rep.Digest
	if err != nil {
		return out, err
	}
	return out, nil
}

// preflight is this connector's own refusals, run before anything is opened.
//
// It resolves the load name the same way neo4j does — Options.RunID, or the job
// that produced the result where the caller named none — and refuses a held
// result. The shared refusals (a reused entity ID, a chunk index used twice, a
// vector naming no chunk) are pkg/preflight's and are asked by sink.Load; this
// is only the part that is this package's contract.
func preflight(res alchemy.Result, o Options) (Options, error) {
	o = o.withDefaults()
	if o.RunID == "" {
		o.RunID = res.Job
	}
	if o.RunID == "" {
		return o, ErrNoRunID
	}
	if len(res.Conflicts) > 0 {
		return o, fmt.Errorf("%w: %d unanswered conflict(s); §7.3 holds the job rather than storing a graph "+
			"that contradicts itself", ErrHeld, len(res.Conflicts))
	}
	// The load name reaches the store as an IRI segment and as a SPARQL
	// literal, both of which escapeSegment and lit handle — but a name that
	// escapes to nothing would produce the same graph IRI for every such load,
	// which is two imports in one graph with nothing saying so.
	if escapeSegment(o.RunID) == "" {
		return o, fmt.Errorf("%w: %q escapes to an empty IRI segment", ErrNoRunID, o.RunID)
	}
	return o, nil
}
