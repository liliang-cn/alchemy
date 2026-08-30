package qdrant

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The central claim of this connector: every record of a result becomes a
// point, chunks carry the vector and nothing else does, and the load says out
// loud that a graph in a vector store is not a graph.
func TestLoadWritesAPointForEveryRecordAndSaysWhatWasLost(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	res := smallResult(8)

	got, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Entities != 2 || got.Relations != 1 || got.Chunks != 2 || got.Vectors != 2 {
		t.Errorf("Loaded = %+v, want 2 entities, 1 relation, 2 chunks, 2 vectors", got)
	}
	if got.Dimension != 8 {
		t.Errorf("Dimension = %d, want 8: the width is learned from the result when the caller did not configure one", got.Dimension)
	}
	for k, want := range map[string]int{"entity": 2, "relation": 1, "chunk": 2, "load": 1} {
		n, err := l.Count(ctx, Filter{Kinds: []string{k}})
		if err != nil {
			t.Fatalf("count %s: %v", k, err)
		}
		if n != want {
			t.Errorf("%s points = %d, want %d", k, n, want)
		}
	}

	// §4's whole argument is that a buyer already has a store; the least
	// defensible thing a connector can do is let them believe this one is a
	// graph database. So the loss is returned, not documented.
	joined := strings.Join(got.Lost, " ")
	if !strings.Contains(joined, "traver") {
		t.Errorf("Loaded.Lost = %q, want it to say that traversal is not stored as traversal", got.Lost)
	}
	if !strings.Contains(joined, "entit") {
		t.Errorf("Loaded.Lost = %q, want it to say entities are not similarity-searchable", got.Lost)
	}
}

// A result with no vectors at all is the ordinary shape of a DDL or graph
// import: no chunks, no embeddings, and a real graph. A vector store is a
// strange home for it, and refusing it would be stranger — the records and
// their provenance are exactly what this store can hold.
func TestAResultWithNoVectorsLoadsIntoAVectorlessCollection(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "t_nodes", Type: "Table", Name: "nodes", Provenance: ddlProv()},
			{ID: "t_conns", Type: "Table", Name: "node_connections", Provenance: ddlProv()},
		},
		Relations: []alchemy.Relation{
			{From: "t_conns", To: "t_nodes", Type: "REFERENCES", Key: "fk_src", Provenance: ddlProv()},
			{From: "t_conns", To: "t_nodes", Type: "REFERENCES", Key: "fk_dst", Provenance: ddlProv()},
		},
		Counts: alchemy.Counts{Entities: 2, Relations: 2, Deterministic: 4},
	}
	got, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Dimension != 0 {
		t.Errorf("Dimension = %d, want 0", got.Dimension)
	}
	// Two foreign keys between the same pair of tables are two edges, not one
	// edge described twice — that is what Relation.Key is for, and a store
	// that keyed identity on {from, to, type} would have written one point.
	if n, err := l.Count(ctx, Filter{Kinds: []string{"relation"}}); err != nil || n != 2 {
		t.Errorf("relation points = %d (err %v), want 2: parallel edges must stay two points", n, err)
	}
	if !strings.Contains(strings.Join(got.Lost, " "), "no embedding") {
		t.Errorf("Loaded.Lost = %q, want it to say this collection can never hold embeddings", got.Lost)
	}
}

func ddlProv() alchemy.Provenance {
	return alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
}
