package verify_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

func TestCheckCanonicalisesTypesAndDoesNotCallThemViolations(t *testing.T) {
	in := verify.Input{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "cluster", Name: "prod"},
			{ID: "n1", Type: "NODE", Name: "node-1"},
		},
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "contains"},
		},
		Vocabulary: vocab(),
		OntologyID: "sds@3",
	}

	got := verify.Check(in)

	if got.Entities[0].Type != "Cluster" || got.Entities[1].Type != "Node" {
		t.Fatalf("entity types = %q, %q; want the ontology's spelling", got.Entities[0].Type, got.Entities[1].Type)
	}
	if got.Relations[0].Type != "CONTAINS" {
		t.Fatalf("relation type = %q, want CONTAINS", got.Relations[0].Type)
	}
	// A type that canonicalises is not a violation: folding without
	// canonicalising only moves the problem, and reporting it as a fault would
	// send a reviewer to fix something the ontology already accepts.
	if len(got.Violations) != 0 {
		t.Fatalf("violations = %+v, want none", got.Violations)
	}
	// The caller's slice must be untouched — Check is a function, not an edit.
	if in.Entities[0].Type != "cluster" {
		t.Fatalf("input entity was mutated: %q", in.Entities[0].Type)
	}
}
