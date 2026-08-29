package verify_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// An edge whose end carries an undeclared type is reported as a relation
// violation too, with the vocabulary's own words pointing at the end rather
// than at the direction. Excluding only the entity would leave the edge
// dangling, so the reviewer needs both findings.
func TestAnEdgeOntoAnUndeclaredTypeIsReportedAsWellAsTheEntity(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "Cluster"},
			{ID: "x1", Type: "Wormhole"},
		},
		Relations:  []alchemy.Relation{{From: "c1", To: "x1", Type: "MENTIONS"}},
		Vocabulary: vocab(),
		OntologyID: "sds@3",
	})

	if len(got.Violations) != 2 {
		t.Fatalf("violations = %+v, want the entity and the edge", got.Violations)
	}
	if got.Violations[0].Kind != alchemy.ViolationUnknownEntityType || got.Violations[1].Kind != alchemy.ViolationRelationNotAllowed {
		t.Fatalf("kinds = %q, %q", got.Violations[0].Kind, got.Violations[1].Kind)
	}
	if !strings.Contains(got.Violations[1].Detail, "not a declared entity type") {
		t.Fatalf("detail = %q, want it to point at the end rather than the direction", got.Violations[1].Detail)
	}
}

// §5b: a rule-breaking item is "not silently dropped and not silently kept".
// Keeping it is what makes exclusion the caller's decision — §7.3's violation
// row says the graph is delivered — and dropping it here would hide the count
// that makes the delivery judgeable.
func TestNothingIsDroppedFromTheGraph(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities:   []alchemy.Entity{{ID: "x1", Type: "Wormhole"}},
		Relations:  []alchemy.Relation{{From: "x1", To: "ghost", Type: "TELEPORTS_TO"}},
		Vocabulary: vocab(),
	})

	if len(got.Entities) != 1 || len(got.Relations) != 1 {
		t.Fatalf("graph = %d entities, %d relations; want the input kept", len(got.Entities), len(got.Relations))
	}
	if got.Counts.Entities != 1 || got.Counts.Relations != 1 {
		t.Fatalf("counts = %+v", got.Counts)
	}
}

// §5: "Supplying an ontology is required for document sources. There is no
// unconstrained mode." A vocabulary that declares nothing therefore rejects
// everything loudly rather than waving it through.
func TestAnEmptyVocabularyRejectsEverything(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities:  []alchemy.Entity{{ID: "c1", Type: "Cluster"}},
		Relations: []alchemy.Relation{{From: "c1", To: "c1", Type: "CONTAINS"}},
	})

	if len(got.Violations) != 2 {
		t.Fatalf("violations = %+v, want both items rejected", got.Violations)
	}
	if !strings.Contains(got.Violations[0].Detail, "the ontology") {
		t.Fatalf("detail = %q, want it readable without an ontology ID", got.Violations[0].Detail)
	}
}

// §5c: verification adds to provenance, it never overwrites it. An item that
// already names the vocabulary it was extracted under keeps that name.
func TestTheOntologyIsStampedOnlyWhereTheProducerLeftItBlank(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "Cluster"},
			{ID: "n1", Type: "Node", Provenance: alchemy.Provenance{Ontology: "sds@2"}},
		},
		Relations:  []alchemy.Relation{{From: "c1", To: "n1", Type: "CONTAINS"}},
		Vocabulary: vocab(),
		OntologyID: "sds@3",
	})

	if got.Entities[0].Provenance.Ontology != "sds@3" {
		t.Fatalf("blank ontology = %q, want it filled in", got.Entities[0].Provenance.Ontology)
	}
	if got.Entities[1].Provenance.Ontology != "sds@2" {
		t.Fatalf("stated ontology = %q, want it left alone", got.Entities[1].Provenance.Ontology)
	}
	if got.Relations[0].Provenance.Ontology != "sds@3" {
		t.Fatalf("relation ontology = %q", got.Relations[0].Provenance.Ontology)
	}
}

// A relation whose ends both satisfy an open-ended type is allowed, and the
// self-loop is not special-cased: the ontology decides, not this package.
func TestOpenEndedRelationsAreAllowedBetweenAnyDeclaredType(t *testing.T) {
	v := ontology.Vocabulary{
		Entities:  []ontology.EntityType{{Name: "Node"}},
		Relations: []ontology.RelationType{{Name: "MENTIONS"}},
	}
	got := verify.Check(verify.Input{
		Entities:   []alchemy.Entity{{ID: "n1", Type: "Node"}},
		Relations:  []alchemy.Relation{{From: "n1", To: "n1", Type: "MENTIONS"}},
		Vocabulary: v,
	})
	if len(got.Violations) != 0 || len(got.Conflicts) != 0 {
		t.Fatalf("violations = %+v conflicts = %+v, want none", got.Violations, got.Conflicts)
	}
}

// A known limit, written down so it is a decision rather than an oversight: two
// inferred sources giving one edge different attribute values is a real
// disagreement, and alchemy.ConflictKind has no member for it. Filing it as a
// contradiction would tell a reviewer a schema is involved when none is, and
// filing it as a direction conflict would be false. Adding a fifth kind means
// changing the shared contract, which is not this package's to change.
func TestTwoInferredSourcesDisagreeingOnAnEdgeAttributeIsNotYetReported(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:n"}, Provenance: fromPDF},
			{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:1"}, Provenance: fromOtherPDF},
		},
		Vocabulary: vocab(),
	})
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v; if this now reports something, the limit is fixed and this test should assert the new kind", got.Conflicts)
	}
}
