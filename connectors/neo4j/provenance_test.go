package neo4j

import (
	"context"
	"testing"
)

// The test DESIGN.md §5b is: a loaded edge can still name its producer, its
// model, its chunk and its ontology. A connector that drops provenance turns
// an attributable edge into an anonymous one and quietly ends the thing the
// buyer paid for, so this is asserted against the database rather than against
// the map that was sent to it.
func TestLoadedEdgeNamesItsProducer(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-P"})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	recs := l.mustQuery(t, "MATCH ()-[r:`USES`]->() WHERE r.`_run` = 'run-P' "+
		"RETURN r.`_producer` AS producer, r.`_model` AS model, r.`_chunk` AS chunk, "+
		"r.`_ontology` AS ontology, r.`_source` AS source, r.`_chunking` AS chunking, "+
		"r.`_confidence` AS confidence, r.`_deterministic` AS deterministic", nil)
	if len(recs) != 1 {
		t.Fatalf("%d USES edges, want 1", len(recs))
	}
	for k, want := range map[string]any{
		"producer": "llm-extract", "model": "gemini-3.6-flash-high", "chunk": int64(14),
		"ontology": "sds@3", "source": "architecture.pdf", "chunking": "heading",
		"confidence": 0.82, "deterministic": false,
	} {
		if recs[0][k] != want {
			t.Fatalf("edge.%s = %#v, want %#v", k, recs[0][k], want)
		}
	}

	// The same guarantee, in the same shape, on a node. It is the same shape
	// because Neo4j cannot hang a node off a relationship, so the shape an
	// edge can support is the shape a node gets.
	recs = l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+" {`_id`:'e2', `_run`:'run-P'}) "+
		"RETURN n.`_producer` AS producer, n.`_source` AS source, n.`_chunk` AS chunk, n.`_deterministic` AS deterministic", nil)
	for k, want := range map[string]any{
		"producer": "ddl", "source": "schema.sql", "chunk": int64(-1), "deterministic": true,
	} {
		if recs[0][k] != want {
			t.Fatalf("node.%s = %#v, want %#v", k, recs[0][k], want)
		}
	}

	// "An agent citing the graph can say which, and a person auditing it can
	// filter to the half that was guessed." That filter is one predicate, on
	// nodes and edges alike, and it is the query this whole layout is for.
	recs = l.mustQuery(t, "MATCH ()-[r]->() WHERE r.`_run` = 'run-P' AND r.`_deterministic` = false RETURN count(r) AS n", nil)
	if recs[0]["n"] != int64(1) {
		t.Fatalf("%v inferred edges, want 1", recs[0]["n"])
	}

	// A chunk index that names nothing is half a guarantee: the record says
	// "chunk 14" and the store must be able to show chunk 14.
	recs = l.mustQuery(t, "MATCH (c:"+mustQuote(t, l.opts.BaseLabel+"Chunk")+" {`_run`:'run-P', `_index`:14}) RETURN c.`_text` AS text", nil)
	if len(recs) != 1 || recs[0]["text"] != "SuperAI uses CortexDB." {
		t.Fatalf("chunk 14 = %v, want the source text the edge points at", recs)
	}
}

func mustQuote(t *testing.T, s string) string {
	t.Helper()
	q, err := quoteIdent(s)
	if err != nil {
		t.Fatalf("quoteIdent(%q): %v", s, err)
	}
	return q
}
