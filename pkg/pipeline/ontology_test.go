package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// testOntology is the prose vocabulary the document tests extract under.
func testOntology(t *testing.T) *ontology.Ontology {
	t.Helper()
	const doc = `{"id":"sds@3","parts":{"prose":{
	  "entities":[{"name":"Cluster","attributes":["region"]},{"name":"Node"}],
	  "relations":[{"name":"DEPLOYED_ON","from":["Cluster"],"to":["Node"]}]}}}`
	o, err := ontology.Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ontology.Load: %v", err)
	}
	return o
}

// §5: "Supplying an ontology is required for document sources. There is no
// unconstrained mode." The 74%→94% story is the argument, and a run that
// started and then produced an unconstrained graph would be shipping the 74%.
// So this is a refusal before the first call, not a degraded mode — failLLM is
// what asserts the "before".
func TestDocumentWithoutAnOntologyIsRefusedBeforeAnyModelIsCalled(t *testing.T) {
	req := Request{
		Sources: []Source{{Name: "architecture.md", Kind: alchemy.SourceDocument, Open: openString("# SuperAI\n\nSuperAI is a cluster.\n")}},
		Models:  alchemy.Models{LLM: &failLLM{t: t}},
	}
	_, err := Run(context.Background(), req, nil)
	if err == nil {
		t.Fatal("Run: want an error for a document source with no ontology, got none")
	}
	if !strings.Contains(err.Error(), "ontology") {
		t.Errorf("error = %q, want it to name the missing ontology", err)
	}
}

// An ontology that does not declare the part the job says it is under is the
// same refusal one step later: ontology.Vocabulary returns an error rather
// than an empty vocabulary precisely so this cannot become an unconstrained
// run, and the pipeline has to make that error arrive before the corpus is
// read rather than after.
func TestUndeclaredPartIsRefusedBeforeAnyModelIsCalled(t *testing.T) {
	req := Request{
		Sources:  []Source{{Name: "architecture.md", Kind: alchemy.SourceDocument, Open: openString("# SuperAI\n")}},
		Ontology: testOntology(t),
		Part:     ontology.PartCode,
		Models:   alchemy.Models{LLM: &failLLM{t: t}},
	}
	_, err := Run(context.Background(), req, nil)
	if err == nil {
		t.Fatal("Run: want an error for a part the ontology does not declare, got none")
	}
}

// A document source with an ontology but no model is the other half of the
// same rule: pkg/alchemy's ports say a stage that needs a nil model fails
// loudly rather than degrading, and failing loudly before the corpus is opened
// is louder still.
func TestDocumentWithoutAnLLMIsRefusedBeforeReading(t *testing.T) {
	req := Request{
		Sources:  []Source{{Name: "architecture.md", Kind: alchemy.SourceDocument, Open: openString("# SuperAI\n")}},
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
	}
	_, err := Run(context.Background(), req, nil)
	if err == nil {
		t.Fatal("Run: want an error for a document source with no LLM, got none")
	}
}
