package cortexdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	cdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// Every test opens its own CortexDB in t.TempDir().
//
// Deliberately not the shared brain at 192.168.123.252, and deliberately not
// gated on an environment variable either. The other two connectors skip
// without a live server because Neo4j and Postgres are servers; CortexDB is a
// file, so a skipped test here would be a test nobody ever runs pretending to
// be one that passes. A local file also means a test cannot pollute a store
// that other agents and machines read.
func openLocal(t *testing.T, o Options) *Loader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alchemy.db")
	l, err := Open(path, o)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return l
}

// db reaches past the Loader for the assertions that have to look at the store
// rather than take the loader's word for it.
func (l *Loader) db() *cdb.DB { return l.cortex }

func countRows(t *testing.T, l *Loader, query string, args ...any) int {
	t.Helper()
	var n int
	if err := l.db().SQL().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func countNodes(t *testing.T, l *Loader) int {
	t.Helper()
	return countRows(t, l, "SELECT COUNT(*) FROM graph_nodes")
}

func countEdges(t *testing.T, l *Loader) int {
	t.Helper()
	return countRows(t, l, "SELECT COUNT(*) FROM graph_edges")
}

// fixture is a small result that exercises every shape the loader has an
// opinion about: two producers, an attribute map, two chunks with real
// vectors, an edge that carries a producer's key, and one of each finding.
func fixture() alchemy.Result {
	llm := alchemy.Provenance{
		Source: "architecture.pdf", Chunk: 0, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading", Confidence: 0.82,
		ReviewedBy: "ada@example.com", RuleSet: "rs-9f21", RuledBy: "authored/type:System",
	}
	ddl := alchemy.Provenance{Source: "architecture.pdf", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI", Attributes: map[string]any{"public": true, "lang": "go"}, Provenance: llm},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			{ID: "e3", Type: "Person", Name: "Ada", Provenance: llm},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Provenance: llm},
			{From: "e3", To: "e1", Type: "WORKS_ON", Attributes: map[string]any{"since": 2024.0}, Provenance: ddl},
		},
		Chunks: []alchemy.Chunk{
			{Index: 0, Text: "SuperAI uses CortexDB.", Source: "architecture.pdf", Strategy: "heading", Heading: "Storage", Start: 100, End: 122},
			{Index: 1, Text: "Ada works on SuperAI.", Source: "architecture.pdf", Strategy: "heading", Heading: "People", Start: 122, End: 143},
		},
		Vectors: []alchemy.Vector{
			{Chunk: 0, Values: unit(8, 0), Model: "embed-4"},
			{Chunk: 1, Values: unit(8, 1), Model: "embed-4"},
		},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownRelationType, Detail: "DEPLOYS is not in sds@3",
			Subject: "e1 -[DEPLOYS]-> e2", Provenance: llm,
		}},
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "CortexDB ~ CortexDB store",
			Detail: "one name is the other with a word added",
			Left:   alchemy.DuplicateSide{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			Right:  alchemy.DuplicateSide{ID: "e1", Type: "System", Name: "SuperAI", Provenance: llm},
		}},
		Guesses: []alchemy.Guess{{Field: "owner_id", ChosenAs: "Person", Alternatives: []string{"Team"}, Reason: "column name", Provenance: ddl}},
		Unread:  []alchemy.Unread{{Source: "architecture.pdf", Locator: "page 9", Reason: "scanned, no OCR model supplied"}},
		// One retirement naming a record this result holds and one naming a
		// record it does not. The second is the ordinary case: the thing being
		// retired is usually in the store from a run that finished last month.
		Supersessions: []alchemy.Supersession{
			{
				Retires: "e2", By: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e1", Type: "System"},
				Reason: "the store was replaced and the old profile still names it",
				Provenance: alchemy.Provenance{
					Source: "correction.md", Chunk: -1, Producer: alchemy.ProducerHuman,
					By: "ana@example.com", At: "2026-03-01T00:00:00Z",
				},
			},
			{
				Retires: "e-from-last-month", By: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e3", Type: "Person"},
				Reason:     "the office changed hands in March",
				Provenance: alchemy.Provenance{Source: "correction.md", Chunk: -1, Producer: alchemy.ProducerHuman, By: "ana@example.com"},
			},
		},
		Counts: alchemy.Counts{
			Entities: 3, Relations: 2, Deterministic: 2, Inferred: 3,
			Violations: 1, Duplicates: 1, Guesses: 1, ChunksEmpty: 2, ChunksUnread: 1,
		},
	}
}

// unit is a deterministic embedding: mostly zero with a 1 at a known index, so
// that "the vector in the store is the vector alchemy computed" is arithmetic
// rather than a guess.
func unit(dim, at int) []float32 {
	v := make([]float32, dim)
	v[at%dim] = 1
	return v
}
