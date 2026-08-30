package verify

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// prov is a producer's own account of one record, short enough to read inline.
func prov(source string, p alchemy.Producer) alchemy.Provenance {
	return alchemy.Provenance{Source: source, Chunk: -1, Producer: p}
}

// A job with no ontology is legal: §5 requires one only for document sources,
// and a DDL-only import declares nothing to check against because the schema
// already stated everything. So an absent vocabulary must mean "no ontology
// rules to break", not "every type is undeclared".
//
// Reporting the second is not merely noisy — it is wrong in the way this
// project exists to prevent. A DDL import would come back with one violation
// per table, all of them saying an ontology nobody supplied does not allow
// something a CREATE TABLE stated, and §5's obligation to report the numbers
// needed to distrust a graph would be discharged with a number that means
// nothing.
func TestNoVocabularyMeansNoOntologyRulesRatherThanNoDeclaredTypes(t *testing.T) {
	in := Input{
		Entities: []alchemy.Entity{
			{ID: "table:orders", Type: "Table", Name: "orders", Provenance: prov("schema.sql", alchemy.ProducerDDL)},
			{ID: "table:customers", Type: "Table", Name: "customers", Provenance: prov("schema.sql", alchemy.ProducerDDL)},
		},
		Relations: []alchemy.Relation{
			{From: "table:orders", To: "table:customers", Type: "REFERENCES", Provenance: prov("schema.sql", alchemy.ProducerDDL)},
		},
	}
	rep := Check(in)
	for _, v := range rep.Violations {
		switch v.Kind {
		case alchemy.ViolationUnknownEntityType, alchemy.ViolationUnknownRelationType, alchemy.ViolationRelationNotAllowed:
			t.Errorf("an absent ontology produced an ontology violation: %s on %s — %s", v.Kind, v.Subject, v.Detail)
		}
	}
	if rep.Counts.Entities != 2 || rep.Counts.Relations != 1 {
		t.Errorf("counts = %d entities / %d relations, want 2/1", rep.Counts.Entities, rep.Counts.Relations)
	}
}

// The structural check is not an ontology rule and must survive: an edge naming
// an entity nobody declared corrupts every walker, whatever vocabulary is or is
// not in force.
func TestADanglingRelationIsStillReportedWithoutAVocabulary(t *testing.T) {
	rep := Check(Input{
		Entities:  []alchemy.Entity{{ID: "table:orders", Type: "Table", Provenance: prov("schema.sql", alchemy.ProducerDDL)}},
		Relations: []alchemy.Relation{{From: "table:orders", To: "table:ghost", Type: "REFERENCES", Provenance: prov("schema.sql", alchemy.ProducerDDL)}},
	})
	var dangling int
	for _, v := range rep.Violations {
		if v.Kind == alchemy.ViolationDanglingRelation {
			dangling++
		}
	}
	if dangling != 1 {
		t.Fatalf("dangling relations reported = %d, want 1: %+v", dangling, rep.Violations)
	}
}

// Almost no conflict is an ontology rule. §7.3 holds a job on two sources
// disagreeing whether or not a vocabulary was ever declared — there is nothing
// in an ontology that could decide between them anyway. Two sources stating
// different columns for one table is that: a question, and no rule.
func TestConflictsAreStillFoundWithoutAVocabulary(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "table:customers", Type: "Table", Attributes: map[string]any{"columns": "id,name,region"}, Provenance: prov("a.sql", alchemy.ProducerDDL)},
			{ID: "table:customers", Type: "Table", Attributes: map[string]any{"columns": "id,name,country"}, Provenance: prov("b.sql", alchemy.ProducerDDL)},
		},
	})
	if len(rep.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1: %+v", len(rep.Conflicts), rep.Conflicts)
	}
}

// The one exception, and the boundary belongs written down next to the rule it
// qualifies. A direction conflict is not "two sources disagree", it is "these
// two records cannot both be true because this relation type runs one way" —
// and the second half of that sentence is an ontology's to say. With no
// ontology nothing said it, so two edges running opposite ways are two facts,
// exactly as an absent vocabulary means no ontology rules rather than no
// declared types.
//
// This is what the deployed service met: a code graph in which two Java classes
// each import the other, 79 times, on a job §7.3 would never let finish. See
// codegraph_test.go for the customer's own graph, and direction_test.go for
// what a declared type still buys.
func TestOppositeDirectionsWithoutAVocabularyAreNotAConflict(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "a.java", Type: "file", Provenance: prov("kg.json", alchemy.ProducerGraphImport)},
			{ID: "b.java", Type: "file", Provenance: prov("kg.json", alchemy.ProducerGraphImport)},
		},
		Relations: []alchemy.Relation{
			{From: "a.java", To: "b.java", Type: "imports", Provenance: prov("kg.json", alchemy.ProducerGraphImport)},
			{From: "b.java", To: "a.java", Type: "imports", Provenance: prov("kg.json", alchemy.ProducerGraphImport)},
		},
	})
	if len(rep.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none: nothing declared %q asymmetric", rep.Conflicts, "imports")
	}
	// And nothing is dropped for it. §5b: not silently dropped and not silently
	// kept — here there is nothing wrong, so both facts are simply kept.
	if len(rep.Relations) != 2 {
		t.Fatalf("relations = %d, want both kept", len(rep.Relations))
	}
}
