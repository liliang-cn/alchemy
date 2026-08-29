package verify_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// vocab is the vocabulary every test in this package checks against. It is
// deliberately the shape §2.1 uses as its example: a closed entity list, two
// relations with declared ends, and one relation with open ends so that the
// "any declared entity type" branch of AllowsRelation is exercised too.
func vocab() ontology.Vocabulary {
	return ontology.Vocabulary{
		Entities: []ontology.EntityType{
			{Name: "Cluster", Attributes: []string{"region", "version"}},
			{Name: "Node"},
			{Name: "StoragePool"},
		},
		Relations: []ontology.RelationType{
			{Name: "CONTAINS", From: []string{"Cluster"}, To: []string{"Node"}},
			{Name: "DEPLOYED_ON", From: []string{"StoragePool"}, To: []string{"Node"}},
			{Name: "MENTIONS"},
		},
	}
}

func TestCheckOfAnEmptyGraphReportsNothing(t *testing.T) {
	got := verify.Check(verify.Input{Vocabulary: vocab(), OntologyID: "sds@3"})

	if len(got.Violations) != 0 || len(got.Conflicts) != 0 {
		t.Fatalf("empty graph produced violations=%v conflicts=%v", got.Violations, got.Conflicts)
	}
	// Non-nil empty slices, not nil: a caller marshalling the report should see
	// "entities": [] rather than null, which reads as "unknown" rather than "none".
	if got.Entities == nil || got.Relations == nil {
		t.Fatalf("entities=%v relations=%v, want non-nil empty slices", got.Entities, got.Relations)
	}
	if (got.Counts != alchemy.Counts{}) {
		t.Fatalf("counts = %+v, want zero", got.Counts)
	}
}
