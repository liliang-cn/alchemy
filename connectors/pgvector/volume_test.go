package pgvector

import (
	"context"
	"errors"
	"testing"
	"time"
)

// §8.4: a big result is not one message, and by the same argument it is not one
// INSERT and not one transaction. This proves the batching is real rather than
// asserting that the totals happen to come out right — the same reason
// pkg/job's store counts its sweep statements.
func TestALargeLoadIsManyStatements(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500})
	seen := map[string]int{}
	l.hooks.afterBatch = func(table string, _ int) error {
		seen[table]++
		return nil
	}
	res := bigResult(2000, 4)
	out, err := l.Load(context.Background(), res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if seen["entities"] != 4 || seen["chunks"] != 4 {
		t.Errorf("statements = %v; 2000 rows at a batch of 500 is 4 each", seen)
	}
	if n := f.count(t, "entities"); n != 2000 {
		t.Errorf("entities = %d, want 2000", n)
	}
	if n := f.count(t, "relations"); n != 1999 {
		t.Errorf("relations = %d, want 1999", n)
	}
	if out.Entities != 2000 {
		t.Errorf("Loaded.Entities = %d, want 2000", out.Entities)
	}
}

// The worst outcome available to a bulk loader is a half-loaded store nobody
// can tell is half-loaded. This is the ordinary version of that failure: the
// load dies between batches, the cleanup runs, and the store is as it was.
func TestAFailureHalfwayLeavesNothing(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500})
	boom := errors.New("the model endpoint's disk filled up")
	// The batch counter moved. It used to be copyRows' own loop over a whole
	// result; the envelope (pkg/sink) now hands this store one batch at a time,
	// so copyRows sees one batch per call and the count that says "die between
	// batches" is kept here. Same failure at the same point — after the second
	// five hundred entities are committed and before the third — asserted by
	// the same row counts below.
	l.hooks.afterBatch = failAfterEntityBatches(l, 2, false, boom)
	_, err := l.Load(context.Background(), bigResult(2000, 4), LoadOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	for _, table := range append([]string{"loads"}, childTables...) {
		if n := f.count(t, table); n != 0 {
			t.Errorf("%s = %d after a failed load, want 0", table, n)
		}
	}
}

// And this is the version that matters: the load dies and the cleanup dies with
// it. Rows are on disk and nobody can reach them, which is the property the
// load row and the views exist to guarantee. A store that is temporarily
// larger is a cleanup problem; a store that is temporarily wrong is a citation
// nobody can explain.
func TestAFailureWhoseCleanupAlsoFailsLeavesAnInvisibleLoad(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500})
	boom := errors.New("the node lost its network")
	// Two batches of entities land, then the loader dies with its cleanup. See
	// failAfterEntityBatches for why the counting moved out of copyRows.
	l.hooks.afterBatch = failAfterEntityBatches(l, 2, true, boom)
	if _, err := l.Load(context.Background(), bigResult(2000, 4), LoadOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}

	if n := f.count(t, "entities"); n != 1000 {
		t.Fatalf("entities on disk = %d, want the 1000 that were committed before the failure", n)
	}
	var state string
	f.scalar(t, &state, `SELECT state FROM {s}.loads`)
	if state != stateLoading {
		t.Errorf("load state = %q, want %q", state, stateLoading)
	}

	// The only thing that matters: nothing can see them.
	fresh := f.open(t, Config{})
	ctx := context.Background()
	var visible int
	f.scalar(t, &visible, `SELECT count(*) FROM {s}.loaded_entities`)
	if visible != 0 {
		t.Errorf("loaded_entities = %d, want 0: a half-written load was readable", visible)
	}
	f.scalar(t, &visible, `SELECT count(*) FROM {s}.loaded_chunks`)
	if visible != 0 {
		t.Errorf("loaded_chunks = %d, want 0", visible)
	}
	hits, err := fresh.Search(ctx, unit(4, 0), 5, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("search returned %d hits from a half-written load", len(hits))
	}

	// An operator asking what is in the store does see it, because they are
	// the person who has to know.
	loads, err := fresh.Loads(ctx)
	if err != nil {
		t.Fatalf("loads: %v", err)
	}
	if len(loads) != 1 || loads[0].Complete {
		t.Fatalf("Loads() = %+v, want one incomplete load", loads)
	}

	// And it is reclaimable without anyone reasoning about which rows to keep.
	swept, err := fresh.Sweep(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(swept.Abandoned) != 1 {
		t.Errorf("swept %d loads, want 1", len(swept.Abandoned))
	}
	for _, table := range append([]string{"loads"}, childTables...) {
		if n := f.count(t, table); n != 0 {
			t.Errorf("%s = %d after the sweep, want 0", table, n)
		}
	}
}

// A sweep must not take a load that is merely slow. The cutoff is applied by
// the database's clock, so two loaders cannot disagree about which loads are
// stale, and one that is still running is safe as long as the cutoff outlasts a
// load.
func TestSweepLeavesAYoungIncompleteLoadAlone(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500})
	boom := errors.New("stop")
	l.hooks.afterBatch = failAfterEntityBatches(l, 2, true, boom)
	_, _ = l.Load(context.Background(), bigResult(2000, 4), LoadOptions{})

	fresh := f.open(t, Config{})
	swept, err := fresh.Sweep(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(swept.Abandoned) != 0 {
		t.Errorf("swept %v; a load that started a moment ago may still be running on another node", swept.Abandoned)
	}
	if _, err := fresh.Sweep(context.Background(), 0); err == nil {
		t.Error("a zero cutoff was accepted; it would delete loads that are still running")
	}
}

// The retry after a crash is Replace, and it has to work over the wreckage the
// crash left rather than requiring somebody to clean up first.
func TestReplaceTakesOverACrashedLoad(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500})
	boom := errors.New("stop")
	l.hooks.afterBatch = failAfterEntityBatches(l, 2, true, boom)
	res := bigResult(2000, 4)
	_, _ = l.Load(context.Background(), res, LoadOptions{ID: "nightly"})

	fresh := f.open(t, Config{Batch: 500})
	ctx := context.Background()
	if _, err := fresh.Load(ctx, res, LoadOptions{ID: "nightly"}); err == nil {
		t.Error("a retry silently joined a half-written load; it has to say Replace")
	}
	out, err := fresh.Load(ctx, res, LoadOptions{ID: "nightly", Replace: true})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if out.Already {
		t.Error("the retry reported Already over a load that never finished")
	}
	if n := f.count(t, "entities"); n != 2000 {
		t.Errorf("entities = %d, want exactly 2000: the crashed half was not replaced but added to", n)
	}
}

// failAfterEntityBatches kills a load once n batches of entities have been
// committed, and takes the pool with it when the failure is meant to look like
// a crashed node.
//
// It exists because the batching moved above this connector. copyRows used to
// walk a whole result and could be told "fail on your second inner batch";
// pkg/sink now hands this store one batch per call, which is the point of the
// envelope — a large graph reaches a store as a stream and never as a struct —
// so the count that used to be copyRows' is the test's. The failure lands at
// the same place it always did: after 1000 of 2000 entities are on disk.
func failAfterEntityBatches(l *Loader, n int, kill bool, boom error) func(string, int) error {
	seen := 0
	return func(table string, _ int) error {
		if table != "entities" {
			return nil
		}
		seen++
		if seen < n {
			return nil
		}
		// Killing the pool is how a crashed loader looks from the database's
		// side: the rows are committed and nothing is coming back to tidy them.
		if kill {
			l.pool.Close()
		}
		return boom
	}
}
