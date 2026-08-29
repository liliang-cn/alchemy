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
// The precondition here was narrowed after pkg/pipeline hit the other half of
// it: §5 requires an ontology for document sources, so a DDL-only job has none
// and every one of its tables was arriving as a violation of rules nobody
// declared. What makes an empty vocabulary a fault is that an ontology was
// *claimed* — an ID with nothing behind it is a vocabulary that failed to
// arrive, and waving that through is exactly the unconstrained mode §5 forbids.
// See rules.governs for the full split.
func TestAnEmptyVocabularyUnderAClaimedOntologyRejectsEverything(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities:   []alchemy.Entity{{ID: "c1", Type: "Cluster"}},
		Relations:  []alchemy.Relation{{From: "c1", To: "c1", Type: "CONTAINS"}},
		OntologyID: "sds@3",
	})

	if len(got.Violations) != 2 {
		t.Fatalf("violations = %+v, want both items rejected", got.Violations)
	}
	if !strings.Contains(got.Violations[0].Detail, `ontology "sds@3"`) {
		t.Fatalf("detail = %q, want it to name the ontology that was claimed", got.Violations[0].Detail)
	}
}

// A violation has to read for a person who supplied a vocabulary inline and
// never named it, which is what an empty Ontology ID means here.
func TestAViolationReadsWithoutAnOntologyID(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities:   []alchemy.Entity{{ID: "c1", Type: "Widget"}},
		Vocabulary: vocab(),
	})
	if len(got.Violations) != 1 {
		t.Fatalf("violations = %+v, want one", got.Violations)
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

// Two sources of equal standing giving one edge different attribute values is
// a question with no schema in it. It is a conflict rather than a violation for
// the usual two reasons: both edges are well-typed, so the ontology has no rule
// to point at, and there is no rule for which of the two to drop either.
func TestTwoInferredSourcesDisagreeingOnAnEdgeAttributeIsAConflict(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:n"}, Provenance: fromPDF},
			{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:1"}, Provenance: fromOtherPDF},
		},
		Vocabulary: vocab(),
	})

	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly one", got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Kind != alchemy.ConflictRelationAttributes {
		t.Fatalf("kind = %q, want %q", c.Kind, alchemy.ConflictRelationAttributes)
	}
	if !strings.Contains(c.Subject, "CONTAINS") || !strings.Contains(c.Subject, "card") {
		t.Fatalf("subject = %q, want it to name the edge and the attribute", c.Subject)
	}
	if !strings.Contains(c.Left.Statement, "1:n") || !strings.Contains(c.Right.Statement, "1:1") {
		t.Fatalf("statements = %q / %q", c.Left.Statement, c.Right.Statement)
	}
	if c.Left.Provenance.Source != "contract.pdf" || c.Right.Provenance.Source != "architecture.pdf" {
		t.Fatalf("claims = %+v / %+v, want each side to keep its own provenance", c.Left, c.Right)
	}
	if len(got.Violations) != 0 {
		t.Fatalf("violations = %+v, want none: both edges are well-typed", got.Violations)
	}
}

// The boundary between the two attribute kinds is standing, and it is written
// out in one table so a later reader cannot collapse them by accident.
//
// ConflictContradiction means one side read a statement and the other inferred
// it — "a schema says otherwise" is the fact that usually settles the question,
// and §5c wants that on the label. ConflictRelationAttributes means neither
// side has that advantage, which is what leaves the question for a person.
//
// Two deterministic sources therefore land on relation_attributes, not on
// contradiction. A schema is involved on *both* sides there, so "the
// deterministic side wins" names no side at all, and filing it as a
// contradiction would send a reviewer looking for an authority that is not in
// the room. It also keeps this family consistent with the direction family,
// where a same-class reversal is already ConflictRelationDirection rather than
// a contradiction — one standing rule for both, rather than two that drift.
func TestTheAttributeConflictKindFollowsStandingNotProducer(t *testing.T) {
	ddlB := alchemy.Provenance{Source: "other.sql", Chunk: -1, Producer: alchemy.ProducerGraphImport}

	for _, tc := range []struct {
		name  string
		left  alchemy.Provenance
		right alchemy.Provenance
		want  alchemy.ConflictKind
	}{
		{"two inferred sources", fromPDF, fromOtherPDF, alchemy.ConflictRelationAttributes},
		{"two deterministic sources", fromSchema, ddlB, alchemy.ConflictRelationAttributes},
		{"a schema against a model", fromSchema, fromPDF, alchemy.ConflictContradiction},
		{"a model against a schema", fromPDF, fromSchema, alchemy.ConflictContradiction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := verify.Check(verify.Input{
				Entities: []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
				Relations: []alchemy.Relation{
					{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:n"}, Provenance: tc.left},
					{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:1"}, Provenance: tc.right},
				},
				Vocabulary: vocab(),
			})
			if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != tc.want {
				t.Fatalf("conflicts = %+v, want one %q", got.Conflicts, tc.want)
			}
			// A contradiction always reads schema-first; an equal-standing
			// conflict reads in the order the claims arrived, because there is no
			// side to promote.
			if tc.want == alchemy.ConflictContradiction && !got.Conflicts[0].Left.Provenance.Producer.Deterministic() {
				t.Fatalf("left = %+v, want the deterministic side first", got.Conflicts[0].Left)
			}
		})
	}
}
