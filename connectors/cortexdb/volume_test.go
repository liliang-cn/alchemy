package cortexdb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// bigResult is a graph too large for one write, so §8.4's "a large result does
// not fit in one message" is exercised rather than argued about.
func bigResult(n, dim int) alchemy.Result {
	res := alchemy.Result{Counts: alchemy.Counts{Entities: n, Relations: n - 1}}
	for i := range n {
		id := fmt.Sprintf("e%05d", i)
		prov := alchemy.Provenance{
			Source: "big.pdf", Chunk: i, Producer: alchemy.ProducerLLMExtract,
			Model: "embed-4", Ontology: "sds@3", Chunking: "fixed", Confidence: 0.5,
		}
		res.Entities = append(res.Entities, alchemy.Entity{ID: id, Type: "Service", Name: id, Provenance: prov})
		res.Chunks = append(res.Chunks, alchemy.Chunk{
			Index: i, Text: fmt.Sprintf("chunk %d talks about %s", i, id),
			Source: "big.pdf", Strategy: "fixed", Start: i * 32, End: i*32 + 32,
		})
		res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: i, Values: unit(dim, i), Model: "embed-4"})
		if i > 0 {
			res.Relations = append(res.Relations, alchemy.Relation{
				From: fmt.Sprintf("e%05d", i-1), To: id, Type: "CALLS", Provenance: prov,
			})
		}
	}
	return res
}

// A load is many writes, and all of them have to land. The batch count is
// asserted too: a test that passed because everything fitted in one write would
// be testing nothing about §8.4.
func TestALargeResultLoadsInBatches(t *testing.T) {
	const n = 2000
	l := openLocal(t, Options{RunID: "run-V", BatchSize: 250})
	rep, err := l.Load(context.Background(), bigResult(n, 8))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Batches < 20 {
		t.Fatalf("%d batches for %d records; the batching did not engage", rep.Batches, n)
	}
	if rep.Entities != n || rep.Relations != n-1 || rep.Chunks != n {
		t.Fatalf("report says %d entities, %d relations, %d chunks; want %d, %d, %d",
			rep.Entities, rep.Relations, rep.Chunks, n, n-1, n)
	}
	// Counted in the store rather than from the report, because the report says
	// what was asked for and what matters is what was written.
	if got := countRows(t, l, "SELECT COUNT(*) FROM graph_nodes WHERE node_type = 'Service'"); got != n {
		t.Fatalf("%d entity nodes in the store, want %d", got, n)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM graph_edges WHERE edge_type = 'CALLS'"); got != n-1 {
		t.Fatalf("%d edges in the store, want %d", got, n-1)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM embeddings"); got != n {
		t.Fatalf("%d chunks in the store, want %d", got, n)
	}
	open, err := l.Incomplete(context.Background())
	if err != nil || len(open) != 0 {
		t.Fatalf("Incomplete = %v, %v after a finished load", open, err)
	}
}

// A dangling relation is ViolationDanglingRelation, and §7.3 puts violations on
// the "returned, graph delivered" side of the line. It is skipped — and named,
// because an edge that disappeared with no record of its disappearance is the
// silent loss this design refuses. CortexDB's foreign key would refuse it too,
// but as a count in the middle of a batch rather than as a name.
func TestADanglingRelationIsSkippedByNameAndTheRestLoads(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-V2"})
	res := fixture()
	res.Relations = append(res.Relations, alchemy.Relation{
		From: "e1", To: "ghost", Type: "USES", Provenance: res.Relations[0].Provenance,
	})
	rep, err := l.Load(context.Background(), res)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rep.SkippedRelations) != 1 {
		t.Fatalf("SkippedRelations = %v, want the one edge whose endpoint is missing", rep.SkippedRelations)
	}
	if rep.Relations != 2 {
		t.Fatalf("%d relations written, want the 2 whose endpoints exist", rep.Relations)
	}
	// The skip is in the store too, not only in the return value: the report is
	// gone the moment the process is, and §5's numbers have to travel with the
	// graph.
	var body string
	if err := l.db().SQL().QueryRowContext(context.Background(),
		"SELECT content FROM documents WHERE id = ?", completionID("run-V2")).Scan(&body); err != nil {
		t.Fatalf("read completion: %v", err)
	}
	if !strings.Contains(body, "ghost") {
		t.Fatalf("the completion document does not name the skipped edge: %s", body)
	}
	if !strings.Contains(body, `"chunks_empty":2`) {
		t.Fatalf("§5's counts are not in the store: %s", body)
	}
}
