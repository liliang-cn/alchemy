package preflight_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/preflight"
)

// Two sources agreeing that Ravel is a Product called Ravel is
// corroboration, not an ID collision — and until this was true, every graph
// merged from more than one source was refused by every store.
//
// Measured, not hypothetical: four documents about one company produced 18 of
// these, and all four connectors refused the load with "18 defect(s) would be
// written as data loss". Every one of the eighteen was two documents agreeing.
func TestTwoSourcesAgreeingAboutOneNodeIsNotAnIDCollision(t *testing.T) {
	res := alchemy.Result{Entities: []alchemy.Entity{
		{ID: "product:ravel", Type: "Product", Name: "Ravel",
			Provenance: alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}},
		{ID: "product:ravel", Type: "Product", Name: "Ravel",
			Provenance: alchemy.Provenance{Source: "docs.md", Chunk: 0, Producer: alchemy.ProducerLLMExtract}},
	}}
	res.Counts = res.Derivable()

	if err := preflight.Refuse(res); err != nil {
		t.Fatalf("a multi-source graph was refused: %v", err)
	}
	var got []preflight.Defect
	for _, d := range preflight.Check(res) {
		if d.Kind == preflight.EntityCorroborated {
			got = append(got, d)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%d corroboration reports, want 1: %+v", len(got), preflight.Check(res))
	}
	if got[0].Severity != preflight.SeverityReport {
		t.Errorf("severity = %v, want a report: the graph is writable and one provenance is lost, "+
			"which is this package's own definition of the difference", got[0].Severity)
	}
}

// And the real collision still refuses. Two records under one ID that disagree
// about what the node IS would leave every edge naming it pointing at whichever
// was written last.
func TestTwoRecordsUnderOneIDThatDisagreeStillRefuse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second alchemy.Entity
	}{
		{"a different type", alchemy.Entity{ID: "x", Type: "Component", Name: "Ravel"}},
		{"a different name", alchemy.Entity{ID: "x", Type: "Product", Name: "Tessera"}},
	} {
		res := alchemy.Result{Entities: []alchemy.Entity{
			{ID: "x", Type: "Product", Name: "Ravel"},
			tc.second,
		}}
		res.Counts = res.Derivable()
		if err := preflight.Refuse(res); err == nil {
			t.Errorf("%s: was written anyway", tc.name)
		}
	}
}

// Attributes are deliberately not part of the comparison. Two sources agreeing
// about what a thing is and disagreeing about what it says is
// verify.ConflictEntityAttributes — a question for a person, not a reason a
// writer cannot write.
func TestDisagreeingAttributesDoNotMakeItACollision(t *testing.T) {
	res := alchemy.Result{Entities: []alchemy.Entity{
		{ID: "x", Type: "Product", Name: "Ravel", Attributes: map[string]any{"version": "1.0"}},
		{ID: "x", Type: "Product", Name: "Ravel", Attributes: map[string]any{"version": "2.0"}},
	}}
	res.Counts = res.Derivable()
	if err := preflight.Refuse(res); err != nil {
		t.Fatalf("refused over an attribute disagreement, which is verify's question: %v", err)
	}
}
