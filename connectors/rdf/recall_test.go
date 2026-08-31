package rdf

import (
	"context"
	"strings"
	"testing"
)

// builders is every read this package makes, built without a server.
//
// A SPARQL query is the kind of code that is otherwise only ever exercised on
// the machines that have a store, and the invariants below are the ones that
// fail silently rather than loudly: an unscoped read answers with another
// import's text, and a read that ignores the marker answers with half of this
// one. Neither looks wrong from the outside.
func builders(l *Loader) map[string]func() (string, error) {
	return map[string]func() (string, error){
		"find an anchor":          func() (string, error) { return l.findSPARQL("ld-1", "ravel", 12) },
		"walk one hop":            func() (string, error) { return l.claimsSPARQL("ld-1", "e1") },
		"resolve a citation":      func() (string, error) { return l.citeSPARQL("ld-1", "architecture.md", 3) },
		"ask what is unanswered":  func() (string, error) { return l.unansweredSPARQL("ld-1", "cortex") },
		"ask what is unanswered2": func() (string, error) { return l.unansweredSPARQL("ld-1", "") },
		"see what contributed":    func() (string, error) { return l.contributionsSPARQL("ld-1", "e1") },
	}
}

func TestEveryReadIsScopedToOneLoadAndToALoadThatFinished(t *testing.T) {
	l := New(Options{})
	graph := l.loadIRI("ld-1")
	for name, build := range builders(l) {
		t.Run(name, func(t *testing.T) {
			q, err := build()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !strings.Contains(q, "GRAPH <"+graph+">") {
				t.Errorf("the query does not open this load's named graph, so it can answer "+
					"from another import of the same file:\n%s", q)
			}
			if !strings.Contains(q, "<"+graph+"> <"+pComplete+"> true") {
				t.Errorf("the query does not require the load marker, so it can answer from a load "+
					"that is still arriving:\n%s", q)
			}
		})
	}
}

// The walk is an inclusion list over predicates, and this is what holds it to
// that.
//
// In RDF the annotation triples are triples: a graph written by this package
// answers `?s ?p ?o` with the edge AND its four-or-more provenance statements,
// whose subject is the quoted triple. Measured on the live store — one edge,
// five rows. A walk that matched any predicate would hand an agent
// skos:closeMatch as a claim about the world, rdfs:label as a claim, and the
// provenance of a claim as a claim.
func TestTheWalkFollowsOnlyPredicatesThisConnectorDeclaredToBeRelationTypes(t *testing.T) {
	q, err := New(Options{}).claimsSPARQL("ld-1", "e1")
	if err != nil {
		t.Fatalf("claimsSPARQL: %v", err)
	}
	if !strings.Contains(q, "?p <"+rdfType+"> <"+clRelationType+">") {
		t.Fatalf("the walk does not restrict its predicates to declared relation types:\n%s", q)
	}
	// And the thing that must never be walked, named so that a change of mind
	// about it fails here rather than in a context pack.
	if strings.Contains(q, skosCloseMatch) {
		t.Errorf("the walk mentions skos:closeMatch; a duplicate finding is a question nobody "+
			"has answered and must not reach an agent in the same struct as a claim:\n%s", q)
	}
}

// The provenance a walk reads has to be the edge's own. Both a node and an edge
// carry a full one here — deliberately, so §5b is one query shape — so a walk
// that read the subject node's would return plausible values on every row and
// attribute every claim about an entity to whatever sentence first named it.
func TestTheWalkReadsTheProvenanceOfTheEdgeAndNotOfItsSubject(t *testing.T) {
	q, err := New(Options{}).claimsSPARQL("ld-1", "e1")
	if err != nil {
		t.Fatalf("claimsSPARQL: %v", err)
	}
	for _, f := range provFields {
		if !strings.Contains(q, "<< ?s ?p ?o >> <"+f.Pred+">") {
			t.Errorf("%s is not read off the quoted triple, so it is either missing or read "+
				"off a node:\n%s", f.Var, q)
		}
	}
}

// pgvector's Search refuses k <= 0 for the same reason: an unbounded anchor
// search over a four-hundred-thousand-record import is a page nobody reads and
// a query nobody meant. There is no "everything" value on purpose.
func TestAnAnchorSearchWithoutALimitIsRefusedRatherThanUnbounded(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := New(Options{}).Find(context.Background(), "ld-1", "ravel", limit); err == nil {
			t.Errorf("Find(limit %d) was accepted", limit)
		}
	}
}

// Every literal a caller supplies reaches a query through the same escaping the
// writer uses. A search term is the one piece of free text in a read, and a
// query is a document a server parses.
func TestASearchTermCannotCloseTheStringItIsIn(t *testing.T) {
	l := New(Options{})
	q, err := l.findSPARQL("ld-1", `x") } INSERT DATA { GRAPH <g> { <a> <b> <c> } } #`, 5)
	if err != nil {
		t.Fatalf("findSPARQL: %v", err)
	}
	if strings.Contains(q, "INSERT DATA") && !strings.Contains(q, `\"`) {
		t.Fatalf("a search term escaped its literal:\n%s", q)
	}
	if !strings.Contains(q, `\"`) {
		t.Errorf("the quote in the search term was not escaped:\n%s", q)
	}
}

// A load name reaches the query as an IRI, so a name that cannot be escaped
// must be refused before it becomes a statement with a hole in it — which the
// server would report as a syntax error attributed to this package rather than
// to the name that caused it.
func TestAReadRefusesALoadNameItCannotPutInAnIRI(t *testing.T) {
	l := New(Options{Base: "http://x/ bad/"})
	for name, build := range builders(l) {
		if _, err := build(); err == nil {
			t.Errorf("%s accepted a base it cannot render as an IRI", name)
		}
	}
}

// The anchor search reports how many matched and not how many came back, and
// the two subselects have to read the same pattern for that number to mean
// anything. Two copies of the pattern would be the four-lists problem in a
// third form: the count would go on being right about a filter the page had
// stopped applying.
func TestTheAnchorCountAndThePageReadOneAndTheSamePattern(t *testing.T) {
	q, err := New(Options{}).findSPARQL("ld-1", "ravel", 12) //nolint:errcheck
	if err != nil {
		t.Fatalf("findSPARQL: %v", err)
	}
	if n := strings.Count(q, "FILTER(CONTAINS(LCASE(?name)"); n != 2 {
		t.Fatalf("the filter appears %d times, want twice — once in the count and once in the page:\n%s", n, q)
	}
	if !strings.Contains(q, "ORDER BY ?name ?id LIMIT 12") {
		t.Errorf("the page is not ordered and limited, so a limit cuts a different place each time:\n%s", q)
	}
}
