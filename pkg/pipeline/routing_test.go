package pipeline

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

const twoTables = `
CREATE TABLE customers (id INT PRIMARY KEY, name TEXT);
CREATE TABLE orders (id INT PRIMARY KEY, customer_id INT REFERENCES customers(id));
`

// §2.1's first lesson, as a routing rule: a CREATE TABLE already states the
// entity and a FOREIGN KEY already states the relation, so a job made only of
// structured sources must reach the end without a model being asked anything.
func TestStructuredSourcesAreNeverSentThroughAModel(t *testing.T) {
	req := Request{
		Sources: []Source{{Name: "schema.sql", Kind: alchemy.SourceDDL, Open: openString(twoTables)}},
		Models:  alchemy.Models{LLM: &failLLM{t: t}, Embedder: &failEmbedder{t: t}},
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("want 2 entities, got %d: %+v", len(res.Entities), res.Entities)
	}
	if len(res.Relations) != 1 {
		t.Fatalf("want 1 relation, got %d: %+v", len(res.Relations), res.Relations)
	}
	if got := res.Relations[0].Provenance.Producer; got != alchemy.ProducerDDL {
		t.Errorf("producer = %q, want %q", got, alchemy.ProducerDDL)
	}
	if len(res.ModelCalls) != 0 {
		t.Errorf("ModelCalls = %+v, want none: a DDL import buys nothing", res.ModelCalls)
	}
}

// An existing graph already asserted everything in it, so the import is
// deterministic (§2.1's table) and its node summaries are the text the
// embedder vectorises — the one place a structured source produces chunks.
const knowledgeGraph = `{
  "nodes": [
    {"id": "file:main.go", "type": "file", "name": "main.go", "summary": "Entry point."},
    {"id": "func:run", "type": "function", "name": "run"}
  ],
  "edges": [{"source": "file:main.go", "target": "func:run", "type": "contains"}]
}`

func TestAnImportedGraphIsDeterministicAndItsSummariesAreEmbedded(t *testing.T) {
	emb := &fakeEmbedder{}
	req := Request{
		Sources: []Source{{Name: "kg.json", Kind: alchemy.SourceGraph, Open: openString(knowledgeGraph)}},
		Models:  alchemy.Models{LLM: &failLLM{t: t}, Embedder: emb},
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Entities) != 2 || len(res.Relations) != 1 {
		t.Fatalf("graph = %d entities, %d relations; want 2 and 1", len(res.Entities), len(res.Relations))
	}
	for _, e := range res.Entities {
		if e.Provenance.Producer != alchemy.ProducerGraphImport {
			t.Errorf("entity %q producer = %q, want %q", e.ID, e.Provenance.Producer, alchemy.ProducerGraphImport)
		}
	}
	if len(res.Chunks) != 1 || len(res.Vectors) != 1 {
		t.Fatalf("%d chunks and %d vectors; the one node with a summary is the one chunk", len(res.Chunks), len(res.Vectors))
	}
	if got := emb.embedded(); len(got) != 1 || got[0] != "Entry point." {
		t.Errorf("embedded %q, want the node summary", got)
	}
}
