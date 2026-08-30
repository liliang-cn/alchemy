package cortexdb

import (
	"context"
	"encoding/json"
	"testing"
)

// edgeID reads back the id CortexDB gave one edge, so the assertions below go
// through CortexDB's own reader rather than through anything this package
// computed.
func edgeID(t *testing.T, l *Loader, edgeType string) string {
	t.Helper()
	var id string
	if err := l.db().SQL().QueryRowContext(context.Background(),
		"SELECT id FROM graph_edges WHERE edge_type = ?", edgeType).Scan(&id); err != nil {
		t.Fatalf("find %s edge: %v", edgeType, err)
	}
	return id
}

func edgeProps(t *testing.T, l *Loader, edgeType string) map[string]any {
	t.Helper()
	var raw string
	if err := l.db().SQL().QueryRowContext(context.Background(),
		"SELECT COALESCE(properties,'{}') FROM graph_edges WHERE edge_type = ?", edgeType).Scan(&raw); err != nil {
		t.Fatalf("read %s edge: %v", edgeType, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

func nodeProps(t *testing.T, l *Loader, id string) map[string]any {
	t.Helper()
	var raw string
	if err := l.db().SQL().QueryRowContext(context.Background(),
		"SELECT COALESCE(properties,'{}') FROM graph_nodes WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatalf("read node %s: %v", id, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

// §5b's guarantee, asked in CortexDB's own words.
//
// CortexDB already has a query for "says who?" — FactProvenanceFor, whose
// Cited() is its own test of whether anything at all backs a fact. A connector
// that put alchemy's provenance somewhere private would leave that query
// answering "uncited" about a graph in which every single edge names its
// source, model and chunk. So the three fields that line up are written through
// CortexDB's fields, and this is the test of it.
func TestLoadedEdgeAnswersCortexDBsOwnProvenanceQuery(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-P"})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	fp, err := l.db().FactProvenanceFor(context.Background(), edgeID(t, l, "USES"), true)
	if err != nil {
		t.Fatalf("FactProvenanceFor: %v", err)
	}
	if !fp.Cited() {
		t.Fatalf("CortexDB calls the edge uncited: %+v", fp)
	}
	if want := documentID("run-P", "architecture.pdf"); fp.DocumentID != want {
		t.Fatalf("document_id = %q, want %q", fp.DocumentID, want)
	}
	if !fp.Inferred {
		t.Fatal("an llm-extract edge is not marked inferred; §5b's filter to the half that was guessed is gone")
	}
	// The point of the chunk id is the text at the other end of it. A citation
	// whose chunk does not resolve is the thing CortexDB reports in Missing,
	// and it is exactly what a connector that let CortexDB re-chunk would
	// produce.
	if len(fp.Missing) != 0 {
		t.Fatalf("chunk ids that resolve to nothing: %v", fp.Missing)
	}
	if len(fp.Chunks) != 1 || fp.Chunks[0].Content != "SuperAI uses CortexDB." {
		t.Fatalf("supporting text = %+v, want alchemy's own chunk", fp.Chunks)
	}
	if fp.Source == "" {
		t.Fatal("CortexDB's free-text provenance is empty; fact_provenance shows a person nothing")
	}

	// CortexDB's rule_id means "this fact was derived by that rule". Alchemy's
	// RuledBy means "a standing policy retyped or renamed this record", which is
	// a different claim about a record that was still read rather than derived.
	// Mapping one onto the other would make every policy-touched edge read as an
	// inference with no evidence of its own.
	if fp.Rule != "" {
		t.Fatalf("rule_id = %q; alchemy's RuledBy must not be laundered into CortexDB's inference rule", fp.Rule)
	}
}

// The six alchemy fields CortexDB has no field for still have to be on the
// record. Nothing in CortexDB's vocabulary asks for them, so they live under
// the reserved prefix — but a wrong edge is only correctable if it is
// attributable, and "which model, under which ontology, at what confidence,
// reviewed by whom" is what attributable means.
func TestFieldsCortexDBHasNoHomeForSurviveTheTrip(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-P2"})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	props := edgeProps(t, l, "USES")
	for k, want := range map[string]string{
		"_model": "gemini-3.6-flash-high", "_ontology": "sds@3", "_chunking": "heading",
		"_confidence": "0.82", "_reviewed_by": "ada@example.com", "_rule_set": "rs-9f21",
		"_ruled_by": "authored/type:System", "_producer": "llm-extract", "_deterministic": "false",
		"_source": "architecture.pdf", "_chunk": "0", "_run": "run-P2",
	} {
		if got, _ := props[k].(string); got != want {
			t.Fatalf("edge property %s = %#v, want %q", k, props[k], want)
		}
	}

	// The same guarantee on a node, in the same shape, so a reader has one
	// vocabulary for both halves of §5b's promise.
	np := nodeProps(t, l, entityNodeID("run-P2", "e2"))
	for k, want := range map[string]string{
		"_producer": "ddl", "_deterministic": "true", "_source": "architecture.pdf",
		"_chunk": "-1", "_id": "e2", "_declared_type": "System",
	} {
		if got, _ := np[k].(string); got != want {
			t.Fatalf("node property %s = %#v, want %q", k, np[k], want)
		}
	}
	// CortexDB's own entity provenance, filled by CortexDB from the document id
	// this connector supplied: a purge of the source can find its entities.
	if _, ok := np["source_document_ids"]; !ok {
		t.Fatal("CortexDB's source_document_ids is absent; a purge by document cannot find this entity")
	}
	// A non-string attribute has to be re-encoded to fit CortexDB's
	// map[string]string, and the re-encoding has to be visible from the data.
	e1 := nodeProps(t, l, entityNodeID("run-P2", "e1"))
	if e1["public"] != "true" || e1["lang"] != "go" {
		t.Fatalf("attributes = %v, want the source's own values", e1)
	}
	if e1["_json_attrs"] != "public" {
		t.Fatalf("_json_attrs = %#v, want the one key that was re-encoded", e1["_json_attrs"])
	}
}
