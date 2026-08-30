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
	// SkipFindings does not skip a retirement. It is a buyer saying they do not
	// want the quality report, and a statement that a record is over is not
	// part of the quality report -- losing it to this flag would drop the one
	// thing in the result that nothing else records.
	if rep.Supersessions != 2 {
		t.Fatalf("Supersessions = %d under SkipFindings, want both: a retirement is not a finding", rep.Supersessions)
	}
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+") WHERE n.`_run`='run-F2' RETURN count(n) AS n", nil)
	// Three entities, the run marker -- which is never optional, because
	// without it there is no way to tell a finished import from an abandoned
	// one -- and the two retirements, which SkipFindings does not cover.
	if recs[0]["n"] != int64(6) {
		t.Fatalf("%v nodes, want 3 entities, the run marker and 2 retirements", recs[0]["n"])
	}
}

// A retirement is loaded, is attributable, and changes nothing about the record
// it names.
//
// The last clause is the one worth a test. alchemy does not act on a
// supersession and a graph store is the one that easily could: a DETACH DELETE
// on the retired node is one line away, and a connector that took it would let
// any producer remove another producer's fact by naming it. So the assertions
// below are that the claim is there, that it says who made it, and that the
// entity it retires is exactly as it was.
func TestARetirementIsRecordedAndNotActedOn(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-S"})
	base := mustQuote(t, l.opts.BaseLabel)
	rep, err := l.Load(context.Background(), fixture())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Supersessions != 2 {
		t.Fatalf("report = %+v, want both retirements", rep)
	}

	// The retired entity is still there, still typed, still with its own
	// provenance. Nothing was deleted and nothing was flagged.
	recs := l.mustQuery(t, "MATCH (n:"+base+" {`_id`:'e2',`_run`:'run-S'}) RETURN n.name AS name, n.`_producer` AS producer", nil)
	if len(recs) != 1 || recs[0]["name"] != "CortexDB" || recs[0]["producer"] != "ddl" {
		t.Fatalf("the retired entity = %v, want it untouched: alchemy states a retirement and never performs one", recs)
	}

	// The claim hangs off the run on its own edge type, so "what did this run
	// find wrong" does not come back with a correction in it.
	recs = l.mustQuery(t, "MATCH (s:"+mustQuote(t, l.opts.BaseLabel+"Supersession")+")-[:STATED_IN]->() WHERE s.`_run`='run-S' "+
		"RETURN s.`_retires` AS retires, s.`_by_id` AS by, s.`_by` AS asserter ORDER BY retires", nil)
	if len(recs) != 2 {
		t.Fatalf("%d retirement nodes, want 2", len(recs))
	}
	if recs[0]["retires"] != "e-from-last-month" || recs[0]["by"] != "e3" || recs[0]["asserter"] != "ana@example.com" {
		t.Fatalf("retirement = %v, want what it retires, what replaces it, and who says so", recs[0])
	}

	// It reaches the record it retires when that record is here, and it does
	// not become an edge between the old record and the new one -- the rule a
	// Duplicate is under, for a sharper reason: an agent walking such an edge
	// would have been handed one producer's decision as if the store had made it.
	recs = l.mustQuery(t, "MATCH (s:"+mustQuote(t, l.opts.BaseLabel+"Supersession")+")-[:RETIRES]->(n) WHERE s.`_run`='run-S' RETURN n.`_id` AS id", nil)
	if len(recs) != 1 || recs[0]["id"] != "e2" {
		t.Fatalf("RETIRES reaches %v, want only e2: the other names a record no longer in this result, which is not an error", recs)
	}
	recs = l.mustQuery(t, "MATCH (:"+base+" {`_id`:'e2',`_run`:'run-S'})-[r]->(:"+base+" {`_id`:'e1',`_run`:'run-S'}) RETURN type(r) AS t", nil)
	for _, r := range recs {
		if r["t"] == "SUPERSEDED_BY" || r["t"] == "REPLACED_BY" {
			t.Fatalf("the retirement became a traversable edge between the two records: %v", recs)
		}
	}
}
