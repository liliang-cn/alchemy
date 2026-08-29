package extract

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func TestExtractReturnsEntitiesWithFullProvenance(t *testing.T) {
	llm := &fakeLLM{name: "gemini-3.6-flash-high", replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"},"confidence":0.82}],
		     "relations":[]}`,
	}}
	got, err := Extract(context.Background(), testChunks("SuperAI is a cluster in eu."), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("want 1 entity, got %d: %#v", len(got.Entities), got.Entities)
	}
	e := got.Entities[0]
	if e.Type != "Cluster" || e.Name != "SuperAI" {
		t.Errorf("entity = %q/%q, want Cluster/SuperAI", e.Type, e.Name)
	}
	if e.Attributes["region"] != "eu" {
		t.Errorf("attributes = %#v, want region=eu", e.Attributes)
	}
	want := alchemy.Provenance{
		Source:     "architecture.md",
		Chunk:      0,
		Producer:   alchemy.ProducerLLMExtract,
		Model:      "gemini-3.6-flash-high",
		Ontology:   "sds@3",
		Chunking:   "heading",
		Confidence: 0.82,
	}
	if e.Provenance != want {
		t.Errorf("provenance =\n%#v\nwant\n%#v", e.Provenance, want)
	}
}
