package cortexdb

import (
	"context"
	"errors"
	"testing"
)

// Loading the same result twice must not double the graph. §5 defers
// incremental re-import, which makes "what does a second load do?" a question
// this connector has to answer rather than inherit.
func TestSecondLoadOfTheSameResultIsANoOp(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-I"})
	ctx := context.Background()
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	n, e := countNodes(t, l), countEdges(t, l)

	rep, err := l.Load(ctx, fixture())
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if !rep.Replay {
		t.Fatal("the second load did not report itself as a replay")
	}
	if got := countNodes(t, l); got != n {
		t.Fatalf("nodes went from %d to %d: the graph doubled", n, got)
	}
	if got := countEdges(t, l); got != e {
		t.Fatalf("edges went from %d to %d: the graph doubled", e, got)
	}
}

// A second, different result under the same run ID is two claims about one
// import with nothing in the data to decide between them. It is refused rather
// than merged, and refusing must leave the store exactly as it was.
func TestDifferentResultUnderTheSameRunIsRefused(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-I2"})
	ctx := context.Background()
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	before := countNodes(t, l)

	changed := fixture()
	changed.Entities[0].Name = "SuperAI Ltd"
	if _, err := l.Load(ctx, changed); !errors.Is(err, ErrRunExists) {
		t.Fatalf("err = %v, want ErrRunExists", err)
	}
	if got := countNodes(t, l); got != before {
		t.Fatalf("a refused load still wrote: %d nodes, was %d", got, before)
	}
	var name string
	if err := l.db().SQL().QueryRowContext(ctx,
		"SELECT content FROM graph_nodes WHERE id = ?", entityNodeID("run-I2", "e1")).Scan(&name); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if name != "SuperAI" {
		t.Fatalf("name = %q, want the first load's value untouched", name)
	}
}

// §8.4: a large result is many writes, so a load can fail with part of it
// written. A half-loaded store is survivable; one nobody can tell is half-loaded
// is not — an operator has to be able to ask, and the answer has to come from
// the store rather than from a log nobody kept.
func TestAHalfLoadedRunSaysSo(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-I3"})
	ctx := context.Background()

	// A result whose second batch cannot be written: the edge names an entity
	// that the store will refuse. Preflight catches a dangling relation, so the
	// failure has to come from CortexDB itself — an ontology this store enforces
	// and this result does not satisfy would do it, and so does a load that is
	// simply interrupted. Interrupting is the honest simulation: the marker is
	// written, the batches are not.
	p, err := preflight(fixture(), l.opts)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	var rep Report
	if _, err := l.claimRun(ctx, p, &rep); err != nil {
		t.Fatalf("claimRun: %v", err)
	}

	open, err := l.Incomplete(ctx)
	if err != nil {
		t.Fatalf("Incomplete: %v", err)
	}
	if len(open) != 1 || open[0] != "run-I3" {
		t.Fatalf("Incomplete = %v, want the run that is mid-import", open)
	}

	// Re-running the same result finishes it, which is the whole point of every
	// write being an upsert keyed on the run.
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	open, err = l.Incomplete(ctx)
	if err != nil {
		t.Fatalf("Incomplete: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("Incomplete = %v after a successful re-Load, want none", open)
	}
}

// The run ID has no default, because both possible defaults are wrong: a
// generated one makes a retry after a crash indistinguishable from a second
// import, and a constant one fuses every import ever run.
func TestRunIDIsRequired(t *testing.T) {
	l := openLocal(t, Options{})
	if _, err := l.Load(context.Background(), fixture()); !errors.Is(err, ErrNoRunID) {
		t.Fatalf("err = %v, want ErrNoRunID", err)
	}
}
