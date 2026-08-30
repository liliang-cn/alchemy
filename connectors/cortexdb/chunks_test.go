package cortexdb

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/cortexdb/v2/pkg/core"
)

// CortexDB chunks text itself, with a size and an overlap. Alchemy chunked
// already, under a strategy that §7.1 makes part of every record's provenance —
// "a graph re-extracted under a different strategy is a different graph".
//
// So the one thing this connector must never do is hand CortexDB the text and
// let it split. The assertion is arithmetic rather than rhetorical: one long
// paragraph that alchemy calls a single chunk stays a single row, where
// CortexDB's own ingest would have made several.
func TestChunkBoundariesAreAlchemysNotCortexDBs(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-C"})
	long := strings.Repeat("SuperAI uses CortexDB, and the paragraph runs on. ", 60)
	prov := alchemy.Provenance{Source: "long.md", Chunk: 0, Producer: alchemy.ProducerLLMExtract, Chunking: "paragraph"}
	res := alchemy.Result{
		Entities: []alchemy.Entity{{ID: "e1", Type: "System", Name: "SuperAI", Provenance: prov}},
		Chunks:   []alchemy.Chunk{{Index: 0, Text: long, Source: "long.md", Strategy: "paragraph", End: len(long)}},
		Vectors:  []alchemy.Vector{{Chunk: 0, Values: unit(8, 0), Model: "embed-4"}},
	}
	rep, err := l.Load(context.Background(), res)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Chunks != 1 {
		t.Fatalf("wrote %d chunks for one alchemy chunk; something re-split the text", rep.Chunks)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM embeddings"); got != 1 {
		t.Fatalf("%d embedding rows for one chunk, want 1", got)
	}
	// Byte-identical, because the chunk is what the provenance points at. Text
	// that was trimmed, re-joined or overlapped is text a citation no longer
	// resolves to.
	var content string
	if err := l.db().SQL().QueryRowContext(context.Background(),
		"SELECT content FROM embeddings WHERE id = ?", chunkNodeID("run-C", 0)).Scan(&content); err != nil {
		t.Fatalf("read chunk: %v", err)
	}
	if content != long {
		t.Fatalf("stored text is %d bytes, alchemy's chunk is %d", len(content), len(long))
	}
}

// §5c: "vectors describe the text that survived review", computed by the model
// the caller named. CortexDB will happily produce a vector of its own — a
// 64-dimensional token hash — and a store holding those while the result claims
// "embed-4" would be making a different claim than the one alchemy returned.
//
// Two things are asserted, and the second is the one that would catch a
// regression: the collection has alchemy's dimension rather than CortexDB's
// default, and a search by a known unit vector returns the chunk that vector was
// computed for. A recomputed geometry fails both.
func TestVectorsArePassedThroughRatherThanRecomputed(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-C2"})
	ctx := context.Background()
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	col, err := l.db().Vector().GetCollection(ctx, defaultCollection)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if col.Dimensions != 8 {
		t.Fatalf("collection dimension = %d, want alchemy's 8 (CortexDB's own default is 64)", col.Dimensions)
	}

	hits, err := l.db().Vector().Search(ctx, unit(8, 1), core.SearchOptions{Collection: defaultCollection, TopK: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits; the vectors alchemy computed are not in the store")
	}
	if hits[0].ID != chunkNodeID("run-C2", 1) {
		t.Fatalf("nearest to chunk 1's own vector is %q, want chunk 1", hits[0].ID)
	}
	if hits[0].Score < 0.99 {
		t.Fatalf("chunk 1 scores %v against its own vector; the stored vector is not the one alchemy computed", hits[0].Score)
	}
	// The model that produced it travels with it, so a store holding two runs
	// embedded by two models can still say which is which.
	if hits[0].Metadata["_model"] != "embed-4" {
		t.Fatalf("chunk metadata = %v, want the embedding model named", hits[0].Metadata)
	}
}

// A chunk with no vector has nowhere to go: CortexDB keeps chunk text in a
// vector row, and the only vector this connector could supply is one it made up.
// Making one up is the recomputation the whole file refuses, so the text stays
// out — and the number is reported, because a citation that silently stopped
// resolving is worse than one that never existed.
func TestChunksWithoutVectorsAreReportedNotInvented(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-C3"})
	res := fixture()
	res.Vectors = nil
	rep, err := l.Load(context.Background(), res)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.ChunksWithoutVectors != 2 || rep.Chunks != 0 {
		t.Fatalf("Chunks=%d ChunksWithoutVectors=%d, want 0 and 2", rep.Chunks, rep.ChunksWithoutVectors)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM embeddings"); got != 0 {
		t.Fatalf("%d embedding rows, want none: a vector was invented", got)
	}
	// The graph is still delivered and still cited, one level coarser: the
	// document survives even when the words do not.
	fp, err := l.db().FactProvenanceFor(context.Background(), edgeID(t, l, "USES"), false)
	if err != nil {
		t.Fatalf("FactProvenanceFor: %v", err)
	}
	if !fp.Cited() || len(fp.ChunkIDs) != 0 {
		t.Fatalf("provenance = %+v, want a document and no chunks", fp)
	}
}

// The mention edges are how §5b's "from which chunk of which file" becomes a
// question CortexDB itself can answer. They exist because this connector passes
// the chunk ids to CortexDB's entity upsert, which builds them — one more thing
// handed over rather than re-implemented.
func TestEntitiesAreLinkedToTheChunksThatMentionedThem(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-C4"})
	rep, err := l.Load(context.Background(), fixture())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.MentionEdges == 0 {
		t.Fatal("no mention edges; nothing in CortexDB connects an entity to the words that produced it")
	}
	if got := countRows(t, l,
		"SELECT COUNT(*) FROM graph_edges WHERE edge_type = 'mentions' AND from_node_id = ?",
		chunkNodeID("run-C4", 0)); got == 0 {
		t.Fatal("chunk 0 mentions nothing, though two entities name it as their chunk")
	}
}
