package verify_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// A direction conflict is an assertion that the relation type is asymmetric —
// that these two records cannot both be true. Something has to have said so,
// and the only thing that ever says what a relation type means is the ontology.
// These tests fix which side of that line each case falls on.

func twoEnds() []alchemy.Entity {
	return []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}}
}

// §5c's own example, and the finding this change must not cost: "a foreign key
// says orders→customer, a document says the customer owns the orders". CONTAINS
// is declared and says nothing about running both ways, so it runs the one way
// the prompt told the model it runs, and a reversal is still a real question.
func TestADeclaredTypeThatSaysNothingStillConflictsWhenReversed(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities: twoEnds(),
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "MENTIONS", Provenance: fromPDF},
			{From: "n1", To: "c1", Type: "MENTIONS", Provenance: fromOtherPDF},
		},
		Vocabulary: vocab(), OntologyID: "sds@3",
	})
	if len(directionConflicts(got.Conflicts)) != 1 {
		t.Fatalf("conflicts = %+v, want one direction conflict: nothing about this change may cost §5c's finding", got.Conflicts)
	}
}

// The declaration that ends it. BothWays says both directions may independently
// be true of one pair, which is what `imports` is and what an ontology author
// needs a word for.
func TestADeclaredBothWaysTypeIsNotAConflictWhenStatedBothWays(t *testing.T) {
	v := ontology.Vocabulary{
		Entities:  []ontology.EntityType{{Name: "file"}},
		Relations: []ontology.RelationType{{Name: "imports", From: []string{"file"}, To: []string{"file"}, BothWays: true}},
	}
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{{ID: "a.java", Type: "file"}, {ID: "b.java", Type: "file"}},
		Relations: []alchemy.Relation{
			{From: "a.java", To: "b.java", Type: "imports", Provenance: fromSchema},
			{From: "b.java", To: "a.java", Type: "imports", Provenance: fromSchema},
		},
		Vocabulary: v, OntologyID: "code@1",
	})
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none: the ontology says this type runs both ways", got.Conflicts)
	}
	// And the reverse edge is not a violation either, or the noise would have
	// moved from the conflict list to the violation list and still held nothing
	// but a person's attention.
	if len(got.Violations) != 0 {
		t.Fatalf("violations = %+v, want none", got.Violations)
	}
}

// A reversal is a contradiction only if the type runs one way. The producers
// decide the *kind* of the finding; whether there is a finding at all is the
// ontology's question, and both branches have to ask it or the gate leaks
// through the one §5c ranks highest.
func TestAReversalOfAnUndeclaredTypeIsNotAContradictionEither(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities: twoEnds(),
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "cites", Provenance: fromSchema},
			{From: "n1", To: "c1", Type: "cites", Provenance: fromPDF},
		},
		Vocabulary: vocab(), OntologyID: "sds@3",
	})
	for _, c := range got.Conflicts {
		if c.Kind == alchemy.ConflictContradiction || c.Kind == alchemy.ConflictRelationDirection {
			t.Fatalf("conflict %+v: nothing declared %q asymmetric, so nothing was contradicted", c, "cites")
		}
	}
}

// The same, one field over: a declared both-ways type reversed by a model is
// two ordinary facts, not a schema being contradicted.
func TestADeclaredBothWaysTypeReversedByAModelIsNotAContradiction(t *testing.T) {
	v := ontology.Vocabulary{
		Entities:  []ontology.EntityType{{Name: "file"}},
		Relations: []ontology.RelationType{{Name: "imports", From: []string{"file"}, To: []string{"file"}, BothWays: true}},
	}
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{{ID: "a.java", Type: "file"}, {ID: "b.java", Type: "file"}},
		Relations: []alchemy.Relation{
			{From: "a.java", To: "b.java", Type: "imports", Provenance: fromSchema},
			{From: "b.java", To: "a.java", Type: "imports", Provenance: fromPDF},
		},
		Vocabulary: v, OntologyID: "code@1",
	})
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", got.Conflicts)
	}
}

// Attributes are a different question from direction and keep their own answer.
// Two sources disagreeing about what one edge *is* stays a conflict whether or
// not the type may run both ways — the declaration is about orientation, and
// reading it as a licence to disagree about everything else would trade one
// silence for a worse one.
func TestABothWaysTypeStillConflictsOnAttributes(t *testing.T) {
	v := ontology.Vocabulary{
		Entities:  []ontology.EntityType{{Name: "file"}},
		Relations: []ontology.RelationType{{Name: "imports", From: []string{"file"}, To: []string{"file"}, BothWays: true}},
	}
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{{ID: "a.java", Type: "file"}, {ID: "b.java", Type: "file"}},
		Relations: []alchemy.Relation{
			{From: "a.java", To: "b.java", Type: "imports", Attributes: map[string]any{"weight": "1"}, Provenance: fromSchema},
			{From: "a.java", To: "b.java", Type: "imports", Attributes: map[string]any{"weight": "2"}, Provenance: fromPDF},
		},
		Vocabulary: v, OntologyID: "code@1",
	})
	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != alchemy.ConflictContradiction {
		t.Fatalf("conflicts = %+v, want one contradiction about the attribute", got.Conflicts)
	}
}
