package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// The two sources of the flagship test. Each states one thing about SuperAI,
// each is internally consistent, and no reader, chunker or extractor working
// on one of them can see anything wrong.
const (
	docEU = "# SuperAI\n\nSuperAI is the cluster in eu-west.\n"
	docUS = "# SuperAI\n\nSuperAI is the cluster in us-east.\n"
)

func twoRegionsLLM() *scriptLLM {
	return &scriptLLM{replies: map[string]string{
		"eu-west": `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"}}],"relations":[]}`,
		"us-east": `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"us"}}],"relations":[]}`,
	}}
}

func regionRequest(t *testing.T, sources ...Source) Request {
	t.Helper()
	return Request{
		Sources:  sources,
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: twoRegionsLLM()},
	}
}

func doc(name, text string) Source {
	return Source{Name: name, Kind: alchemy.SourceDocument, Open: openString(text)}
}

// This is the test the package exists for. §8.1: "a conflict is two sources
// disagreeing — and only something that sees *both* can notice. Spread one
// job's sources across five nodes and the disagreement between source 1 and
// source 4 is visible to nobody: every node finishes cleanly, and the merged
// graph contradicts itself."
//
// So the two halves are asserted together. Each source alone finishes clean —
// that is what makes the disagreement invisible to anything smaller than the
// job — and the job holding both finds it.
func TestAConflictIsVisibleOnlyToSomethingHoldingTheWholeJob(t *testing.T) {
	for _, src := range []Source{doc("eu.md", docEU), doc("us.md", docUS)} {
		res, err := Run(context.Background(), regionRequest(t, src), nil)
		if err != nil {
			t.Fatalf("Run(%s alone): %v; each source is internally clean", src.Name, err)
		}
		if len(res.Conflicts) != 0 {
			t.Fatalf("Run(%s alone) found %d conflicts, want none", src.Name, len(res.Conflicts))
		}
	}

	_, err := Run(context.Background(), regionRequest(t, doc("eu.md", docEU), doc("us.md", docUS)), nil)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("Run(both) = %v, want a *HeldError: the two sources disagree", err)
	}
	if len(held.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %d: %+v", len(held.Conflicts), held.Conflicts)
	}
	c := held.Conflicts[0]
	if c.Kind != alchemy.ConflictEntityAttributes {
		t.Errorf("kind = %q, want %q", c.Kind, alchemy.ConflictEntityAttributes)
	}
	// The reviewer has to see a file on each side; a conflict that named the
	// job and not the two sources would be unanswerable.
	if c.Left.Provenance.Source == c.Right.Provenance.Source {
		t.Errorf("both claims name %q; the two sides must name the two sources", c.Left.Provenance.Source)
	}
}
