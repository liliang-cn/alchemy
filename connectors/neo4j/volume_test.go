package neo4j

import (
	"context"
	"fmt"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// big builds a result of n entities and n-1 relations chaining them, which is
// the smallest thing that exercises batching in both writers.
func big(n int) alchemy.Result {
	prov := alchemy.Provenance{Source: "dump.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
	res := alchemy.Result{Counts: alchemy.Counts{Entities: n, Relations: n - 1}}
	for i := range n {
		res.Entities = append(res.Entities, alchemy.Entity{
			ID: fmt.Sprintf("e%d", i), Type: "Row", Name: fmt.Sprintf("row %d", i), Provenance: prov,
		})
		if i > 0 {
			res.Relations = append(res.Relations, alchemy.Relation{
				From: fmt.Sprintf("e%d", i-1), To: fmt.Sprintf("e%d", i), Type: "NEXT", Provenance: prov,
			})
		}
	}
	return res
}

// §8.4: a large result does not fit in one message and does not fit in one
// transaction either. There is one code path rather than a small-result
// shortcut, so the case that matters is not the untested one.
func TestLargeResultIsBatched(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-V", BatchSize: 500, SkipFindings: true})
	res := big(2000)
	rep, err := l.Load(context.Background(), res)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 4 entity batches + 4 relation batches (1999 edges) + the completion.
	if rep.Batches < 8 {
		t.Fatalf("Batches = %d, want at least 8: a 2000-record load went in one transaction", rep.Batches)
	}
	if rep.Entities != 2000 || rep.Relations != 1999 {
		t.Fatalf("report = %d entities, %d relations", rep.Entities, rep.Relations)
	}
	if got := nodeCount(t, l, "run-V"); got != 2001 {
		t.Fatalf("%d nodes, want 2000 entities plus the run marker", got)
	}
	if got := edgeCount(t, l, "run-V"); got != 1999 {
		t.Fatalf("%d edges, want 1999", got)
	}
}

// A failure halfway through is the case §8.4 forces on us, and the worst
// available outcome is a half-loaded graph nobody can tell is half-loaded. The
// failure here is real rather than injected: a uniqueness constraint the load
// walks into at a batch boundary.
func TestFailureMidLoadLeavesTheRunMarkedIncomplete(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-W", BatchSize: 500, SkipFindings: true})
	base := mustQuote(t, l.opts.BaseLabel)
	ctx := context.Background()

	// A constraint the third batch will violate, and a node already holding
	// the ID it will try to create under a different run.
	constraint := mustQuote(t, "alchemy_test_unique_"+l.opts.BaseLabel)
	l.mustQuery(t, "CREATE CONSTRAINT "+constraint+" IF NOT EXISTS FOR (n:"+base+") REQUIRE n.`_id` IS UNIQUE", nil)
	t.Cleanup(func() { l.mustQuery(t, "DROP CONSTRAINT "+constraint+" IF EXISTS", nil) })
	l.mustQuery(t, "CREATE (n:"+base+" {`_id`:'e1200', `_run`:'somebody-elses-run'})", nil)

	res := big(2000)
	if _, err := l.Load(ctx, res); err == nil {
		t.Fatal("Load succeeded through a constraint violation")
	}

	// The graph is partial, and it says so. This is the whole point: one query
	// finds every import that died halfway.
	recs := l.mustQuery(t, "MATCH (r:"+base+":"+mustQuote(t, l.opts.BaseLabel+"Run")+" {`_id`:'run-W'}) RETURN r.`_complete` AS complete", nil)
	if len(recs) != 1 || recs[0]["complete"] != false {
		t.Fatalf("run marker = %v, want one marked incomplete", recs)
	}
	partial := nodeCount(t, l, "run-W")
	if partial <= 1 || partial >= 2001 {
		t.Fatalf("%d nodes after a mid-load failure, want a partial graph", partial)
	}

	// And it is finishable by running the same load again, because every
	// write is a MERGE on an identity the result decides. A retry that is not
	// a retry is the reason CREATE was not used.
	l.mustQuery(t, "MATCH (n:"+base+" {`_run`:'somebody-elses-run'}) DETACH DELETE n", nil)
	l.mustQuery(t, "DROP CONSTRAINT "+constraint+" IF EXISTS", nil)

	rep, err := l.Load(ctx, res)
	if err != nil {
		t.Fatalf("resuming Load: %v", err)
	}
	if !rep.Replay {
		t.Fatal("the resumed load did not recognise the run it was finishing")
	}
	if got := nodeCount(t, l, "run-W"); got != 2001 {
		t.Fatalf("%d nodes after the retry, want 2000 entities plus the marker — the retry doubled or lost records", got)
	}
	if got := edgeCount(t, l, "run-W"); got != 1999 {
		t.Fatalf("%d edges after the retry, want 1999", got)
	}
	recs = l.mustQuery(t, "MATCH (r:"+base+":"+mustQuote(t, l.opts.BaseLabel+"Run")+" {`_id`:'run-W'}) RETURN r.`_complete` AS complete", nil)
	if recs[0]["complete"] != true {
		t.Fatalf("run still marked incomplete after a successful retry")
	}
}
