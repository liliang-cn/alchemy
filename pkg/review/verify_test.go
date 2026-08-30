package review_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

func vocab() ontology.Vocabulary {
	return ontology.Vocabulary{
		Entities: []ontology.EntityType{{Name: "Cluster"}, {Name: "Node"}, {Name: "StoragePool"}},
		Relations: []ontology.RelationType{
			{Name: "CONTAINS", From: []string{"Cluster"}, To: []string{"Node"}},
			{Name: "DEPLOYED_ON", From: []string{"StoragePool"}, To: []string{"Node"}},
		},
	}
}

// Queue matches findings back onto the records that produced them by building
// the verifier's own subject strings rather than by parsing them, so it has to
// be checked against the real verifier: a subject this package cannot rebuild
// is an item with no targets, and a decision on it would change nothing while
// reporting that it had.
func TestEveryFindingTheRealVerifierProducesFindsItsRecords(t *testing.T) {
	entities := []alchemy.Entity{
		{ID: "c1", Type: "Cluster", Name: "prod", Provenance: fromSchema},
		{ID: "n1", Type: "Node", Name: "node-1", Provenance: fromSchema},
		{ID: "n1", Type: "StoragePool", Name: "node-1", Provenance: fromPDF},
		{ID: "w1", Type: "Widget", Name: "w", Provenance: fromPDF},
	}
	relations := []alchemy.Relation{
		{From: "c1", To: "n1", Type: "CONTAINS", Provenance: fromSchema},
		{From: "n1", To: "c1", Type: "CONTAINS", Provenance: fromPDF},
		{From: "c1", To: "n1", Type: "MENTIONS", Provenance: fromPDF},
		{From: "c1", To: "ghost", Type: "CONTAINS", Provenance: fromPDF},
	}
	rep := verify.Check(verify.Input{
		Entities: entities, Relations: relations, Vocabulary: vocab(), OntologyID: "sds@3",
	})
	if len(rep.Conflicts) == 0 || len(rep.Violations) == 0 {
		t.Fatalf("fixture produced conflicts=%d violations=%d, want both", len(rep.Conflicts), len(rep.Violations))
	}
	res := alchemy.Result{
		Entities: rep.Entities, Relations: rep.Relations,
		Conflicts: rep.Conflicts, Violations: rep.Violations, Counts: rep.Counts,
	}

	items := review.Queue(rep, res, review.Options{Reviewing: true, MinConfidence: 0.9})
	// Two conflicts (a retyped entity, a reversed edge) and four violations
	// (an undeclared entity type, an undeclared relation type, a relation
	// between types it is not allowed between, and a dangling edge). Pinning
	// the number is what makes the loop below a check rather than a formality.
	if len(items) != 6 {
		t.Fatalf("queue = %d items, want 6", len(items))
	}
	var decisions []review.Decision
	for _, it := range items {
		// A dangling relation names an endpoint the result does not contain,
		// but the edge itself is a record and must still be reachable.
		if len(it.Targets) == 0 {
			t.Fatalf("item %+v found no record to act on", it)
		}
		decisions = append(decisions, review.Decision{ItemID: it.ID, Verb: review.VerbAccept, By: "ana"})
	}

	got, _, err := review.Apply(res, items, decisions)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if open := got.Held(); len(open) != 0 {
		t.Fatalf("held = %+v, want every conflict answered", open)
	}
	for _, v := range got.Violations {
		if v.Provenance.ReviewedBy != "ana" {
			t.Fatalf("violation %+v was answered but does not say by whom", v)
		}
	}
}
