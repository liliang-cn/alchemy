package graphimport_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/source/graphimport"
)

// A call graph names two call sites from one function to another, and gives
// each edge its own id — which is the only thing in the document that says they
// are two edges rather than one edge written twice. It belongs on Relation.Key:
// left in the attributes it reads as two sources disagreeing about one edge's
// id, which is the shape that stalled a real import from the DDL side.
//
// Understand-Anything's knowledge-graph.json and CortexDB's side graphs both
// write edges as objects that may carry members beyond the endpoints, and "id"
// is the member every one of them spells the same way — the same reason the
// node side accepts only that spelling.
func TestAnEdgesOwnIDIsItsKey(t *testing.T) {
	const doc = `{
  "nodes": [
    {"id": "func:run", "type": "function", "name": "run"},
    {"id": "func:log", "type": "function", "name": "log"}
  ],
  "edges": [
    {"id": "call:run:12", "source": "func:run", "target": "func:log", "type": "calls", "line": 12},
    {"id": "call:run:47", "source": "func:run", "target": "func:log", "type": "calls", "line": 47}
  ]
}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("relations = %d, want both call sites", len(res.Relations))
	}
	if res.Relations[0].Key != "call:run:12" || res.Relations[1].Key != "call:run:47" {
		t.Fatalf("keys = %q, %q; want the ids the document gave", res.Relations[0].Key, res.Relations[1].Key)
	}
	for i, r := range res.Relations {
		if _, ok := r.Attributes["id"]; ok {
			t.Errorf("relation %d carries %q as an attribute, which was already read into a field", i, "id")
		}
		if r.Attributes["line"] == nil {
			t.Errorf("relation %d lost what the document stated beyond its identity: %#v", i, r.Attributes)
		}
	}
}

// An edge that states no id keeps the identity it always had: from, to and
// type. A document that gives its edges nothing to be told apart by has said
// nothing this package can invent.
func TestAnEdgeWithNoIDHasNoKey(t *testing.T) {
	const doc = `{"nodes": [{"id": "a"}, {"id": "b"}], "edges": [{"source": "a", "target": "b", "type": "calls"}]}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Relations[0].Key != "" {
		t.Fatalf("key = %q, want none", res.Relations[0].Key)
	}
}
