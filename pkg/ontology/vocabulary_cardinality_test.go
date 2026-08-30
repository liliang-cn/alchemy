package ontology_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// The cardinality accessors must resolve a type the same way every other
// accessor does, or a vocabulary that declares CHIEF_TECHNOLOGY_OFFICER_OF
// would stop constraining an extractor that emitted a different spelling of
// it — and a constraint that silently stops applying is worse than one that
// was never declared, because the graph goes on reporting itself clean.
func TestCardinalityResolvesATypeTheSameWayEveryOtherAccessorDoes(t *testing.T) {
	v := ontology.Vocabulary{
		Entities: []ontology.EntityType{{Name: "Person"}, {Name: "Organization"}},
		Relations: []ontology.RelationType{{
			Name: "CHIEF_TECHNOLOGY_OFFICER_OF", From: []string{"Person"}, To: []string{"Organization"},
			AtMostOneIn: true,
		}},
	}
	for _, spelling := range []string{"CHIEF_TECHNOLOGY_OFFICER_OF", "chief_technology_officer_of"} {
		canonical, ok := v.CanonicalRelation(spelling)
		if !ok {
			t.Fatalf("CanonicalRelation(%q) did not resolve; this test's premise is wrong", spelling)
		}
		if !v.HoldsAtMostOneIn(spelling) {
			t.Errorf("HoldsAtMostOneIn(%q) is false while CanonicalRelation resolves it to %q; "+
				"the two accessors disagree about what a type is", spelling, canonical)
		}
	}
	if v.HoldsAtMostOneOut("CHIEF_TECHNOLOGY_OFFICER_OF") {
		t.Error("at_most_one_out was not declared and must not be inferred from at_most_one_in")
	}
	// A type nobody declared constrains nothing: it is already a violation,
	// and making it a cardinality conflict too would report one fault twice.
	if v.HoldsAtMostOneIn("REPORTS_TO") || v.HoldsAtMostOneOut("REPORTS_TO") {
		t.Error("an undeclared type must constrain nothing")
	}
}
