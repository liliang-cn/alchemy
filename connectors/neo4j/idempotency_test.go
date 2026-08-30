package neo4j

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func nodeCount(t *testing.T, l *Loader, run string) int64 {
	t.Helper()
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+") WHERE n.`_run` = $run RETURN count(n) AS n", map[string]any{"run": run})
	return recs[0]["n"].(int64)
}

func edgeCount(t *testing.T, l *Loader, run string) int64 {
	t.Helper()
	recs := l.mustQuery(t, "MATCH ()-[r]->() WHERE r.`_run` = $run RETURN count(r) AS n", map[string]any{"run": run})
	return recs[0]["n"].(int64)
}

// Loading the same result twice must not double the graph. §5 defers
// incremental re-import, which makes "what does a second load do?" a question
// this connector has to answer rather than inherit.
func TestSecondLoadOfTheSameResultIsANoOp(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-I"})
	ctx := context.Background()
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	n, e := nodeCount(t, l, "run-I"), edgeCount(t, l, "run-I")

	rep, err := l.Load(ctx, fixture())
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if !rep.Replay {
		t.Fatal("the second load did not report itself as a replay")
	}
	if got := nodeCount(t, l, "run-I"); got != n {
		t.Fatalf("nodes went from %d to %d: the graph doubled", n, got)
	}
	if got := edgeCount(t, l, "run-I"); got != e {
		t.Fatalf("edges went from %d to %d: the graph doubled", e, got)
	}
}

// A second, different result under the same run ID is two claims about one
// import with nothing in the data to decide between them. It is refused rather
// than merged, and refusing must leave the graph exactly as it was.
func TestDifferentResultUnderTheSameRunIsRefused(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-I2"})
	ctx := context.Background()
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	before := nodeCount(t, l, "run-I2")

	changed := fixture()
	changed.Entities[0].Name = "SuperAI Ltd"
	_, err := l.Load(ctx, changed)
	if !errors.Is(err, ErrRunExists) {
		t.Fatalf("err = %v, want ErrRunExists", err)
	}
	if got := nodeCount(t, l, "run-I2"); got != before {
		t.Fatalf("a refused load still wrote: %d nodes, was %d", got, before)
	}
	// The old name is still the one in the graph: a refusal that half-applied
	// would be worse than either outcome.
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+" {`_id`:'e1',`_run`:'run-I2'}) RETURN n.name AS name", nil)
	if recs[0]["name"] != "SuperAI" {
		t.Fatalf("name = %v, want the first load's value untouched", recs[0]["name"])
	}
}

// Overwrite is how a caller says "I know it changed and I mean to replace it".
// It has to actually replace: a node the new result no longer contains must be
// gone, not left behind looking current.
func TestOverwriteReplacesTheRun(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-I3", Overwrite: true})
	ctx := context.Background()
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	smaller := fixture()
	smaller.Entities = smaller.Entities[:2]
	smaller.Relations = smaller.Relations[:1]
	smaller.Duplicates = nil
	if _, err := l.Load(ctx, smaller); err != nil {
		t.Fatalf("Overwrite Load: %v", err)
	}
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+") WHERE n.`_run`='run-I3' AND n.`_id`='e3' RETURN count(n) AS n", nil)
	if recs[0]["n"] != int64(0) {
		t.Fatalf("the entity the new result dropped is still in the graph")
	}
	if got := edgeCount(t, l, "run-I3"); got != 1 {
		t.Fatalf("%d edges after overwrite, want 1", got)
	}
}

// The crux. Entity.ID is stable within one result and says nothing across
// runs, so two runs that both call something "e1" are two different things.
// Fusing them would be entity resolution done on a key that cannot carry it —
// a MERGE on the wrong key, which is the failure that looks like success.
func TestTwoRunsAreTwoGraphs(t *testing.T) {
	label := testLabel(t)
	a := liveLoader(t, Options{RunID: "run-a", BaseLabel: label})
	b := liveLoader(t, Options{RunID: "run-b", BaseLabel: label})
	ctx := context.Background()

	prov := alchemy.Provenance{Source: "a.pdf", Chunk: 0, Producer: alchemy.ProducerLLMExtract}
	first := alchemy.Result{Entities: []alchemy.Entity{{ID: "e1", Type: "System", Name: "Acme", Provenance: prov}}}
	second := alchemy.Result{Entities: []alchemy.Entity{{ID: "e1", Type: "Person", Name: "Ada", Provenance: prov}}}
	if _, err := a.Load(ctx, first); err != nil {
		t.Fatalf("Load a: %v", err)
	}
	if _, err := b.Load(ctx, second); err != nil {
		t.Fatalf("Load b: %v", err)
	}
	recs := a.mustQuery(t, "MATCH (n:"+mustQuote(t, label)+") WHERE n.`_id` = 'e1' RETURN n.name AS name ORDER BY name", nil)
	if len(recs) != 2 {
		t.Fatalf("%d nodes for two runs' e1, want 2: an ID that means nothing across runs was used to join them", len(recs))
	}
	if recs[0]["name"] != "Acme" || recs[1]["name"] != "Ada" {
		t.Fatalf("nodes = %v", recs)
	}
}

// Two chunks asserting the same edge are two pieces of evidence, and §5b's
// promise is that each names its own producer. One merged edge can only name
// one of them.
func TestTwoAssertionsOfOneEdgeStayTwoEdges(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-I5"})
	res := fixture()
	twin := res.Relations[0]
	twin.Provenance.Chunk = 15
	res.Relations = append(res.Relations, twin)
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load: %v", err)
	}
	recs := l.mustQuery(t, "MATCH ()-[r:`USES`]->() WHERE r.`_run`='run-I5' RETURN r.`_chunk` AS chunk ORDER BY chunk", nil)
	if len(recs) != 2 || recs[0]["chunk"] != int64(14) || recs[1]["chunk"] != int64(15) {
		t.Fatalf("USES edges = %v, want one per chunk that asserted it", recs)
	}
}
