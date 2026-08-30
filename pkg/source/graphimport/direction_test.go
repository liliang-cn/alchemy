package graphimport_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/source/graphimport"
)

// Understand-Anything writes "direction": "forward" on every edge it emits —
// all 21854 of them in the graph this fixture came from — and it is a statement
// about the edge, not a fact about the world it points at. So it is read into a
// slot like from, to and type, and does not survive as an attribute: a member
// this package has consumed must not be re-exported under its own name, or the
// graph carries two spellings of one thing (see object.rest).
func TestForwardIsReadRatherThanKeptAsAnAttribute(t *testing.T) {
	const doc = `{
	  "nodes": [{"id": "a"}, {"id": "b"}],
	  "edges": [{"source": "a", "target": "b", "type": "imports", "direction": "forward", "weight": 1.0}]
	}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := res.Relations[0]
	if r.From != "a" || r.To != "b" {
		t.Fatalf("edge = %s -> %s, want a -> b: \"forward\" states the edge runs as written", r.From, r.To)
	}
	if _, ok := r.Attributes["direction"]; ok {
		t.Errorf("attributes carry %q, which was already read into a slot: %#v", "direction", r.Attributes)
	}
	if r.Attributes["weight"] != 1.0 {
		t.Errorf("attributes = %#v, want the members no slot claimed to survive", r.Attributes)
	}
}

// Most graph documents state no direction at all — CortexDB's three shapes
// never do — and an absent member states nothing, exactly as it does for every
// other slot here. Reading silence as anything else would refuse the documents
// this package was written for.
func TestAnEdgeStatingNoDirectionIsUnchanged(t *testing.T) {
	const doc = `{"nodes": [{"id": "a"}, {"id": "b"}],
	  "edges": [{"from": "a", "to": "b", "type": "owns"}]}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r := res.Relations[0]; r.From != "a" || r.To != "b" {
		t.Fatalf("edge = %s -> %s, want a -> b", r.From, r.To)
	}
}

// A direction this package does not know is refused, and this is the decision
// worth arguing about, because the alternative is what it used to do: let the
// value fall into the attributes and import the edge as written.
//
// "backward" could mean the edge runs target -> source; "both" could mean it
// runs each way; either reading produces a different graph from the one written
// down, and nothing in the document says which was meant. An edge read
// backwards is not locally excludable the way a dangling one is — it points
// somewhere plausible and wrong, which is §2.1's bug with a three-month fuse —
// so the document is refused rather than read under a coin flip. This is the
// same rule AmbiguityError already applies to a slot spelled two ways.
func TestADirectionThisPackageDoesNotKnowIsRefused(t *testing.T) {
	for _, value := range []string{"backward", "reverse", "both", "undirected", "1"} {
		t.Run(value, func(t *testing.T) {
			doc := `{"nodes": [{"id": "a"}, {"id": "b"}],
			  "edges": [{"source": "a", "target": "b", "type": "imports", "direction": "` + value + `"}]}`
			_, err := graphimport.Parse("kg.json", strings.NewReader(doc))
			if err == nil {
				t.Fatalf("Parse accepted direction %q; want a refusal", value)
			}
			var de *graphimport.DirectionError
			if !errors.As(err, &de) {
				t.Fatalf("error %v (%T), want *graphimport.DirectionError", err, err)
			}
			if de.Value != value {
				t.Errorf("Value = %q, want %q", de.Value, value)
			}
			if !strings.Contains(de.Error(), "edge 0") {
				t.Errorf("error %q does not say which edge", de.Error())
			}
			if !strings.Contains(de.Error(), "forward") {
				t.Errorf("error %q does not say what it does understand", de.Error())
			}
		})
	}
}

// Case is a spelling wobble, not a claim. "Forward" and "forward" are one word,
// the same way relation type names fold in pkg/ontology.
func TestDirectionFoldsCase(t *testing.T) {
	const doc = `{"nodes": [{"id": "a"}, {"id": "b"}],
	  "edges": [{"source": "a", "target": "b", "type": "imports", "direction": "Forward"}]}`
	if _, err := graphimport.Parse("kg.json", strings.NewReader(doc)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

// What direction does NOT say is whether the relation type may run both ways.
// Every edge of a mutual pair is written "forward" — both files really do
// import each other — and the two records are two facts, not a producer
// declaring its ontology. Whether `imports` is asymmetric is an ontology's
// statement to make (pkg/ontology's RelationType.BothWays); §5 keeps the
// ontology an input, and inferring one here from the shape of the data would be
// the automatic ontology generation it rules out.
func TestDirectionDoesNotDeclareTheTypeSymmetric(t *testing.T) {
	const doc = `{
	  "nodes": [{"id": "a"}, {"id": "b"}],
	  "edges": [
	    {"source": "a", "target": "b", "type": "imports", "direction": "forward"},
	    {"source": "b", "target": "a", "type": "imports", "direction": "forward"}
	  ]}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("relations = %d, want both directions kept as stated", len(res.Relations))
	}
	if res.Relations[0].From != "a" || res.Relations[1].From != "b" {
		t.Fatalf("relations = %+v; each edge runs the way the document wrote it", res.Relations)
	}
}
