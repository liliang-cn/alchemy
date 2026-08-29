package pipeline

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// §3's diagram for the one hard kind: a document goes through the reader, then
// the chunker, then the model — and what comes out the far end carries the
// provenance §5b makes a product guarantee, including which vocabulary
// constrained it and which chunking produced the chunk it was read from.
func TestDocumentIsReadChunkedAndExtractedUnderTheOntology(t *testing.T) {
	llm := &scriptLLM{name: "gemini-3.6-flash-high", replies: map[string]string{
		"SuperAI": `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"},"confidence":0.82}],"relations":[]}`,
	}}
	req := Request{
		Sources:  []Source{{Name: "architecture.md", Kind: alchemy.SourceDocument, Open: openString("# Overview\n\nSuperAI is a cluster in eu.\n")}},
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: llm},
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("want the chunks the extractor read, got none")
	}
	if len(res.Entities) != 1 {
		t.Fatalf("want 1 entity, got %d: %+v", len(res.Entities), res.Entities)
	}
	got := res.Entities[0]
	if got.Type != "Cluster" || got.Name != "SuperAI" {
		t.Errorf("entity = %q/%q, want Cluster/SuperAI", got.Type, got.Name)
	}
	p := got.Provenance
	if p.Source != "architecture.md" {
		t.Errorf("provenance source = %q, want architecture.md", p.Source)
	}
	if p.Producer != alchemy.ProducerLLMExtract {
		t.Errorf("producer = %q, want %q", p.Producer, alchemy.ProducerLLMExtract)
	}
	if p.Model != "gemini-3.6-flash-high" {
		t.Errorf("model = %q, want the model that was called", p.Model)
	}
	if p.Ontology != "sds@3" {
		t.Errorf("ontology = %q, want sds@3", p.Ontology)
	}
	if p.Chunking == "" {
		t.Error("chunking is empty; §7.1 makes the strategy part of the provenance")
	}
	if llm.called() == 0 {
		t.Error("the model was never called for a document source")
	}
}
