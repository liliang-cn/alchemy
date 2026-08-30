package neo4j

import (
	"context"
	"testing"
)

// §5's obligation is that the numbers needed to distrust a graph travel with
// it. After a load the reader is holding a graph, so the findings have to be
// answerable in Cypher — otherwise they are answerable only in a JSON file on
// the laptop of whoever ran the import.
func TestFindingsAreQueryable(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-F"})
	base := mustQuote(t, l.opts.BaseLabel)
	rep, err := l.Load(context.Background(), fixture())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Violations != 1 || rep.Duplicates != 1 || rep.Guesses != 1 || rep.Unread != 1 || rep.Chunks != 1 {
		t.Fatalf("report = %+v, want one of each finding", rep)
	}

	// Everything a run found hangs off the run, so "what is wrong with this
	// import" is one hop from the thing that names the import.
	recs := l.mustQuery(t, "MATCH (f)-[:FOUND_IN]->(:"+base+":"+mustQuote(t, l.opts.BaseLabel+"Run")+" {`_id`:'run-F'}) "+
		"RETURN count(f) AS n", nil)
	if recs[0]["n"] != int64(4) {
		t.Fatalf("%v findings hang off the run, want 4 (a chunk is material the run read, not something it found)", recs[0]["n"])
	}

	// A violation names its subject, and reaches it when the subject is a node
	// this result contains.
	recs = l.mustQuery(t, "MATCH (v:"+mustQuote(t, l.opts.BaseLabel+"Violation")+") WHERE v.`_run`='run-F' "+
		"RETURN v.`_kind` AS kind, v.`_detail` AS detail, v.`_producer` AS producer", nil)
	if recs[0]["kind"] != "unknown_relation_type" || recs[0]["producer"] != "llm-extract" {
		t.Fatalf("violation = %v", recs[0])
	}

	// A Duplicate is not an edge between the two nodes. An agent traversing a
	// MAY_BE_SAME_AS relationship has been handed a claim; the finding says
	// only that a signal fired and nobody has decided.
	recs = l.mustQuery(t, "MATCH (a:"+base+" {`_id`:'e2',`_run`:'run-F'})-[r]-(b:"+base+" {`_id`:'e1',`_run`:'run-F'}) "+
		"WHERE r.`_type` IS NULL RETURN type(r) AS t", nil)
	if len(recs) != 0 {
		t.Fatalf("the duplicate finding became a traversable edge between the two nodes: %v", recs)
	}
	recs = l.mustQuery(t, "MATCH (d:"+mustQuote(t, l.opts.BaseLabel+"Duplicate")+")-[:CANDIDATE]->(n) WHERE d.`_run`='run-F' RETURN n.`_id` AS id ORDER BY id", nil)
	if len(recs) != 2 || recs[0]["id"] != "e1" || recs[1]["id"] != "e2" {
		t.Fatalf("candidates = %v, want both nodes reachable from the finding", recs)
	}

	// A finding must not be reachable by walking the graph a buyer came for.
	recs = l.mustQuery(t, "MATCH (n:"+base+" {`_id`:'e2',`_run`:'run-F'})-->(x) RETURN count(x) AS n", nil)
	if recs[0]["n"] != int64(0) {
		t.Fatalf("%v nodes reachable outward from an entity, want 0: findings leaked into the graph", recs[0]["n"])
	}

	// Vectors are not loaded, and the report says how many were left, because
	// a silent drop is the one thing this design refuses everywhere else.
	if rep.SkippedVectors != 0 {
		t.Fatalf("SkippedVectors = %d for a fixture with none", rep.SkippedVectors)
	}
}

// A buyer who wants only the graph should not pay for the corpus or the
// commentary.
func TestSkipOptions(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-F2", SkipChunks: true, SkipFindings: true})
	rep, err := l.Load(context.Background(), fixture())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Chunks != 0 || rep.Violations != 0 {
		t.Fatalf("report = %+v, want nothing but the graph", rep)
	}
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+") WHERE n.`_run`='run-F2' RETURN count(n) AS n", nil)
	// Three entities plus the run marker, which is never optional: without it
	// there is no way to tell a finished import from an abandoned one.
	if recs[0]["n"] != int64(4) {
		t.Fatalf("%v nodes, want 3 entities and the run marker", recs[0]["n"])
	}
}
