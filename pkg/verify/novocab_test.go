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

// Conflicts are not an ontology rule either. §7.3 holds a job on two sources
// disagreeing whether or not a vocabulary was ever declared — there is nothing
// in an ontology that could decide between them anyway.
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
