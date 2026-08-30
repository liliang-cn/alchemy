package verify_test

import (
	"os"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/source/ddl"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// schemaVocab is the vocabulary a DDL import is checked against: one entity
// type and one relation type, because that is all a CREATE TABLE and a FOREIGN
// KEY state (see pkg/source/ddl).
func schemaVocab() ontology.Vocabulary {
	return ontology.Vocabulary{
		Entities:  []ontology.EntityType{{Name: ddl.EntityType}},
		Relations: []ontology.RelationType{{Name: ddl.RelationType, From: []string{ddl.EntityType}, To: []string{ddl.EntityType}}},
	}
}

// A whole schema imports without a single question for a person.
//
// This is the test the defect was found by. A customer's own schema, run
// through the deployed service, came back with 89 conflicts and a job stuck in
// NEEDS_REVIEW. Every one of them was this package inventing a disagreement
// between two foreign keys that agree about everything they are actually about
// — STATION_LINKS references STATIONS twice, once for each end of a link, and
// both constraints are correct.
//
// The fixture is synthetic and reproduces that schema's shape rather than its
// content; see testdata. It is asserted on a whole schema rather than on an
// excerpt because that is what the defect was: no rule was broken by any one
// statement, so nothing smaller than a schema exhibits it.
func TestTheSchemaImportsWithoutConflicts(t *testing.T) {
	text, err := os.ReadFile("testdata/freight-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ddl.Parse("freight-schema.sql", string(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The reader's own conflicts are checked first so a failure below cannot be
	// blamed on the schema contradicting itself.
	if len(parsed.Conflicts) != 0 {
		t.Fatalf("the reader found %d conflicts of its own: %+v", len(parsed.Conflicts), parsed.Conflicts)
	}

	got := verify.Check(verify.Input{
		Entities:   parsed.Entities,
		Relations:  parsed.Relations,
		Vocabulary: schemaVocab(),
		OntologyID: "schema@1",
	})
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %d, want 0; first is %+v", len(got.Conflicts), got.Conflicts[0])
	}
	if got.Counts.Conflicts != 0 {
		t.Fatalf("counts.conflicts = %d, want 0", got.Counts.Conflicts)
	}
}

// The two ends of a link survive as two edges. Zero conflicts is also what
// deleting one of them would produce, so the count above is only half the
// claim.
func TestTheTwoEndsOfALinkAreTwoEdges(t *testing.T) {
	text, err := os.ReadFile("testdata/freight-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ddl.Parse("freight-schema.sql", string(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := verify.Check(verify.Input{
		Entities:   parsed.Entities,
		Relations:  parsed.Relations,
		Vocabulary: schemaVocab(),
		OntologyID: "schema@1",
	})

	var ends []alchemy.Relation
	for _, r := range got.Relations {
		if r.From == "table:station_links" && r.To == "table:stations" {
			ends = append(ends, r)
		}
	}
	if len(ends) != 2 {
		t.Fatalf("station_links -> stations = %d edges, want 2: %+v", len(ends), ends)
	}
	names := map[string]bool{}
	for _, r := range ends {
		c, _ := r.Attributes["constraint"].(string)
		names[c] = true
	}
	if !names["FK_SL_STATIONS_SRC"] || !names["FK_SL_STATIONS_DST"] {
		t.Fatalf("constraints = %v, want both ends of the link", names)
	}
}
