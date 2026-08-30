package neo4j

import (
	"context"
	"testing"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// The load a buyer will actually do: hand over a result, get a graph.
func TestLoadWritesTheGraph(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-A"})
	rep, err := l.Load(context.Background(), fixture())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Entities != 3 || rep.Relations != 2 {
		t.Fatalf("report says %d entities and %d relations, want 3 and 2", rep.Entities, rep.Relations)
	}

	base, _ := quoteIdent(l.opts.BaseLabel)
	recs := l.mustQuery(t, "MATCH (n:"+base+":`System`) WHERE n._run = $run RETURN n.name AS name ORDER BY name", map[string]any{"run": "run-A"})
	if len(recs) != 2 || recs[0]["name"] != "CortexDB" || recs[1]["name"] != "SuperAI" {
		t.Fatalf("System nodes = %v, want CortexDB and SuperAI", recs)
	}

	// An ontology type is a label, and it is the only label besides the base
	// one: a buyer's `MATCH (p:Person)` has to be the query they expect.
	recs = l.mustQuery(t, "MATCH (n:"+base+" {_id:'e3', _run:'run-A'}) RETURN labels(n) AS labels", nil)
	got := recs[0]["labels"].([]any)
	if len(got) != 2 {
		t.Fatalf("labels = %v, want exactly the base label and the ontology type", got)
	}

	// A relation type is a relationship type.
	recs = l.mustQuery(t, "MATCH (:"+base+" {_id:'e1',_run:'run-A'})-[r:`USES`]->(:"+base+" {_id:'e2',_run:'run-A'}) RETURN type(r) AS t, r.`_type` AS declared", nil)
	if len(recs) != 1 || recs[0]["t"] != "USES" || recs[0]["declared"] != "USES" {
		t.Fatalf("USES edge = %v, want exactly one", recs)
	}

	// Attributes land at the top level, where a buyer reads them.
	recs = l.mustQuery(t, "MATCH (n:"+base+" {_id:'e1',_run:'run-A'}) RETURN n.public AS public", nil)
	if recs[0]["public"] != true {
		t.Fatalf("attribute public = %v, want true", recs[0]["public"])
	}
	recs = l.mustQuery(t, "MATCH ()-[r:`WORKS_ON`]->() WHERE r._run='run-A' RETURN r.since AS since", nil)
	if recs[0]["since"] != 2024.0 {
		t.Fatalf("relation attribute since = %#v, want 2024.0", recs[0]["since"])
	}

	// A nested attribute becomes JSON text, and the node says which keys that
	// happened to, so a reader can tell JSON from a string the source wrote.
	recs = l.mustQuery(t, "MATCH (n:"+base+" {_id:'e3',_run:'run-A'}) RETURN n.address AS address, n.`_json_attrs` AS encoded", nil)
	if recs[0]["address"] != `{"city":"Wien"}` {
		t.Fatalf("address = %#v", recs[0]["address"])
	}
	if enc, ok := recs[0]["encoded"].([]any); !ok || len(enc) != 1 || enc[0] != "address" {
		t.Fatalf("_json_attrs = %#v, want [address]", recs[0]["encoded"])
	}
}

// The run marker is how a buyer tells a finished import from one that died
// halfway. It is written before the first record and completed after the last.
func TestLoadMarksTheRunComplete(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-A"})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	runLabel, _ := quoteIdent(l.opts.BaseLabel + "Run")
	recs := l.mustQuery(t, "MATCH (r:"+base+":"+runLabel+" {`_id`:'run-A'}) RETURN r", nil)
	if len(recs) != 1 {
		t.Fatalf("%d run nodes, want 1", len(recs))
	}
	props := recs[0]["r"].(driver.Node).Props
	if props["_complete"] != true {
		t.Fatalf("_complete = %v after a clean load", props["_complete"])
	}
	if props["_digest"] != digest(fixture()) {
		t.Fatalf("_digest = %v, want the result's digest", props["_digest"])
	}
	// §5's obligation: the numbers needed to distrust the graph travel with
	// the graph, not with the JSON on somebody's laptop.
	for k, want := range map[string]any{
		"_count_entities": int64(3), "_count_relations": int64(2),
		"_count_deterministic": int64(2), "_count_inferred": int64(3),
		"_count_violations": int64(1), "_count_duplicates": int64(1),
		"_count_chunks_empty": int64(2), "_count_chunks_unread": int64(1),
	} {
		if props[k] != want {
			t.Fatalf("run.%s = %#v, want %#v", k, props[k], want)
		}
	}
}
