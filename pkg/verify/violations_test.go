package verify_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

func TestUnknownEntityTypeIsAViolationCarryingItsOwnProvenance(t *testing.T) {
	prov := alchemy.Provenance{Source: "architecture.pdf", Chunk: 14, Producer: alchemy.ProducerLLMExtract, Model: "gemini-3.6-flash-high"}
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "Cluster", Name: "prod"},
			{ID: "x1", Type: "Wormhole", Name: "w", Provenance: prov},
		},
		Vocabulary: vocab(),
		OntologyID: "sds@3",
	})

	if len(got.Violations) != 1 {
		t.Fatalf("violations = %+v, want exactly one", got.Violations)
	}
	v := got.Violations[0]
	if v.Kind != alchemy.ViolationUnknownEntityType {
		t.Fatalf("kind = %q, want %q", v.Kind, alchemy.ViolationUnknownEntityType)
	}
	if v.Subject != "x1" {
		t.Fatalf("subject = %q, want the entity ID", v.Subject)
	}
	// The reviewer has to be able to act on this without opening the code: it
	// must name the offending type and the vocabulary that rejected it.
	for _, want := range []string{"Wormhole", "sds@3", "Cluster"} {
		if !strings.Contains(v.Detail, want) {
			t.Fatalf("detail %q does not mention %q", v.Detail, want)
		}
	}
	// §5b: the violation is returned "with the chunk that produced it".
	if v.Provenance.Source != "architecture.pdf" || v.Provenance.Chunk != 14 {
		t.Fatalf("provenance = %+v, want the offending entity's own", v.Provenance)
	}
}

// graph is the well-formed two-entity graph the relation violation tests bolt
// a single broken edge onto, so that each test's failure is the edge and never
// the scenery.
func graph(rels ...alchemy.Relation) verify.Input {
	return verify.Input{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "Cluster", Name: "prod"},
			{ID: "n1", Type: "Node", Name: "node-1"},
		},
		Relations:  rels,
		Vocabulary: vocab(),
		OntologyID: "sds@3",
	}
}

func TestUnknownRelationTypeIsAViolation(t *testing.T) {
	prov := alchemy.Provenance{Source: "ops.md", Chunk: 2, Producer: alchemy.ProducerLLMExtract}
	got := verify.Check(graph(alchemy.Relation{From: "c1", To: "n1", Type: "TELEPORTS_TO", Provenance: prov}))

	if len(got.Violations) != 1 || got.Violations[0].Kind != alchemy.ViolationUnknownRelationType {
		t.Fatalf("violations = %+v, want one unknown_relation_type", got.Violations)
	}
	v := got.Violations[0]
	// Subject is the edge, in the form §7.3's reviewer reads it.
	if v.Subject != "c1 -[TELEPORTS_TO]-> n1" {
		t.Fatalf("subject = %q", v.Subject)
	}
	if !strings.Contains(v.Detail, "TELEPORTS_TO") || !strings.Contains(v.Detail, "CONTAINS") {
		t.Fatalf("detail %q must name the rejected type and the declared ones", v.Detail)
	}
	if v.Provenance.Chunk != 2 {
		t.Fatalf("provenance = %+v, want the relation's own", v.Provenance)
	}
}

func TestRelationBetweenTheWrongEndpointTypesIsAViolation(t *testing.T) {
	got := verify.Check(graph(alchemy.Relation{From: "n1", To: "c1", Type: "CONTAINS"}))

	if len(got.Violations) != 1 || got.Violations[0].Kind != alchemy.ViolationRelationNotAllowed {
		t.Fatalf("violations = %+v, want one relation_not_allowed", got.Violations)
	}
	// The vocabulary already writes a reason for a person; repeating it here in
	// worse words would be a second, divergent explanation of the same rule.
	want, _ := vocab().AllowsRelation("CONTAINS", "Node", "Cluster")
	if want {
		t.Fatal("fixture is wrong: CONTAINS Node->Cluster should not be allowed")
	}
	_, reason := vocab().AllowsRelation("CONTAINS", "Node", "Cluster")
	if got.Violations[0].Detail != reason {
		t.Fatalf("detail = %q, want the vocabulary's own reason %q", got.Violations[0].Detail, reason)
	}
}

func TestRelationNamingAnAbsentEntityIsADanglingViolation(t *testing.T) {
	got := verify.Check(graph(alchemy.Relation{From: "c1", To: "ghost", Type: "CONTAINS"}))

	if len(got.Violations) != 1 || got.Violations[0].Kind != alchemy.ViolationDanglingRelation {
		t.Fatalf("violations = %+v, want one dangling_relation", got.Violations)
	}
	if !strings.Contains(got.Violations[0].Detail, "ghost") {
		t.Fatalf("detail %q must name the missing entity", got.Violations[0].Detail)
	}
}

// A dangling edge is not also checked for endpoint types: there is no type to
// check, and a second violation saying so would send the reviewer to widen the
// ontology for an entity that does not exist.
func TestDanglingRelationIsReportedOnceEvenWithBothEndsMissing(t *testing.T) {
	got := verify.Check(graph(alchemy.Relation{From: "gone", To: "ghost", Type: "CONTAINS"}))

	if len(got.Violations) != 1 {
		t.Fatalf("violations = %+v, want exactly one", got.Violations)
	}
	d := got.Violations[0].Detail
	if !strings.Contains(d, "gone") || !strings.Contains(d, "ghost") {
		t.Fatalf("detail %q must name both missing ends", d)
	}
}
