package cortexdb

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func loadErr(t *testing.T, run string, res alchemy.Result) string {
	t.Helper()
	l := openLocal(t, Options{RunID: run})
	_, err := l.Load(context.Background(), res)
	if err == nil {
		t.Fatal("Load succeeded, want a refusal")
	}
	if got := countNodes(t, l); got != 0 {
		t.Fatalf("%d nodes written by a refused load, want none", got)
	}
	return err.Error()
}

// Everything alchemy knows lives under the reserved prefix, everything CortexDB
// knows lives under the names it chose, and everything the source said lives
// outside both. The rule is enforced rather than resolved because both ways of
// resolving it are worse: letting the attribute win overwrites the provenance
// §5b promises, and letting alchemy win drops something the source actually
// said, which the buyer's ontology declared and nothing downstream would report.
func TestAnAttributeInAlchemysNamespaceIsRefused(t *testing.T) {
	res := fixture()
	res.Entities[0].Attributes = map[string]any{"_producer": "me"}
	if msg := loadErr(t, "run-A", res); !strings.Contains(msg, "ReservedPrefix") {
		t.Fatalf("error %q does not name the knob that frees the namespace", msg)
	}
}

// The same rule against CortexDB's own property names, which is the collision a
// Neo4j-shaped connector would not have: `name`, `description` and
// `source_document_ids` are CortexDB's, and an attribute landing on one of them
// would not collide loudly — it would quietly change what CortexDB thinks the
// record is.
func TestAnAttributeOnCortexDBsOwnPropertyIsRefused(t *testing.T) {
	res := fixture()
	res.Entities[0].Attributes = map[string]any{"source_document_ids": "elsewhere"}
	if msg := loadErr(t, "run-A2", res); !strings.Contains(msg, "source_document_ids") {
		t.Fatalf("error %q does not name the colliding property", msg)
	}

	res = fixture()
	res.Relations[0].Attributes = map[string]any{"chunk_ids": "elsewhere"}
	if msg := loadErr(t, "run-A3", res); !strings.Contains(msg, "chunk_ids") {
		t.Fatalf("error %q does not name the colliding property", msg)
	}
}

// CortexDB's node upsert is ON CONFLICT DO UPDATE, so two entities of one result
// sharing an ID would leave the second silently wearing the first's edges.
func TestTwoEntitiesWithOneIDAreRefused(t *testing.T) {
	res := fixture()
	res.Entities[2].ID = "e1"
	if msg := loadErr(t, "run-A4", res); !strings.Contains(msg, "overwrite") {
		t.Fatalf("error %q does not say what would have happened", msg)
	}
}

// A collection holds one dimension, so two embedding models in one result is a
// result the caller has to split — and finding that out halfway through a write
// is finding it out too late.
func TestTwoEmbeddingDimensionsAreRefusedUpFront(t *testing.T) {
	res := fixture()
	res.Vectors[1].Values = unit(16, 1)
	if msg := loadErr(t, "run-A5", res); !strings.Contains(msg, "one dimension") {
		t.Fatalf("error %q does not explain the refusal", msg)
	}
}

// SkipChunks is a real loss and has to read as one: the graph still loads, the
// facts are still attributable to a document, and the words are gone.
func TestSkipChunksLeavesTheGraphCitedOnlyToItsDocuments(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-A6", SkipChunks: true})
	rep, err := l.Load(context.Background(), fixture())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Chunks != 0 || rep.MentionEdges != 0 {
		t.Fatalf("Chunks=%d MentionEdges=%d, want 0 and 0", rep.Chunks, rep.MentionEdges)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM embeddings"); got != 0 {
		t.Fatalf("%d embeddings written with SkipChunks", got)
	}
	fp, err := l.db().FactProvenanceFor(context.Background(), edgeID(t, l, "USES"), false)
	if err != nil {
		t.Fatalf("FactProvenanceFor: %v", err)
	}
	if !fp.Cited() || len(fp.ChunkIDs) != 0 {
		t.Fatalf("provenance = %+v, want a document and no chunks", fp)
	}
	// The graph itself is whole: skipping the corpus is not skipping the import.
	if rep.Entities != 3 || rep.Relations != 2 {
		t.Fatalf("report = %+v, want the whole graph", rep)
	}
}
