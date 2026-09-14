package cortexdb

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
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
	if _, _, err := l.claimRun(ctx, p.digest, false, &rep); err != nil {
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

// TestAFailedReplaceLeavesTheOldGraphStanding is the data loss this had.
//
// Replace deleted the whole run in Begin and wrote the new graph in Commit,
// which is two phases with the entire stream between them. Anything that failed
// in the second — the model, the network, the process — left the old graph gone
// and the new one absent, and there is no third place the facts were. Measured
// on a deployed store: seven nodes and six edges became one node and none.
//
// Nor does the package's own recovery argument cover it. "Every write is an
// upsert, so a crashed load is finishable by running it again" needs the result
// to run again with, and alchemy holds work in progress rather than a
// catalogue — an hour later the job is gone and the old graph is not coming
// back either.
//
// So the delete moves after the write. A crash between them now leaves the old
// records beside the new ones, which is a state a re-run converges out of; the
// one state that cannot be recovered from is the one where nothing is there.
func TestAFailedReplaceLeavesTheOldGraphStanding(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-R1"})
	ctx := context.Background()
	if _, err := l.Load(ctx, fixture()); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	before := countNodes(t, l)
	if before == 0 {
		t.Fatal("the fixture wrote nothing, so this test would pass vacuously")
	}

	// A replace whose write cannot finish. Two records CortexDB calls one edge,
	// carrying two different producer keys — ErrParallelEdges, which a
	// streaming caller reaches in Commit, which is after Begin and therefore
	// after the delete this test is about.
	broken := fixture()
	broken.Entities[0].Name = "SuperAI Ltd"
	broken.Relations = append(broken.Relations,
		alchemy.Relation{From: "e1", To: "e2", Type: "USES", Key: "fk_left", Provenance: broken.Relations[0].Provenance},
		alchemy.Relation{From: "e1", To: "e2", Type: "USES", Key: "fk_right", Provenance: broken.Relations[0].Provenance},
	)
	if _, err := sink.Load(ctx, l, broken, sink.Options{Load: "run-R1", Replace: true}); err == nil {
		t.Fatal("the broken replace succeeded; this test needs a load that fails after Begin")
	}

	if got := countNodes(t, l); got < before {
		t.Fatalf("a failed replace left %d nodes where there were %d: the old graph was deleted "+
			"before the new one was written, and neither is there now", got, before)
	}
}
