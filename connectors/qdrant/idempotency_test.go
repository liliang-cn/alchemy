package qdrant

import (
	"context"
	"errors"
	"testing"
)

// The question a connector has to answer before anybody trusts it with a
// nightly job: what does loading the same result twice do? Here it must do
// nothing, and it must do nothing cheaply — the second call finds the
// fingerprint already complete and never writes a point.
func TestLoadingTheSameResultTwiceChangesNothing(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	res := smallResult(8)

	first, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	before, err := l.Count(ctx, Filter{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	second, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !second.Already {
		t.Error("the second load did not report Already; a retried nightly job must be a no-op, not a doubling")
	}
	if second.ID != first.ID {
		t.Errorf("second load landed under %q, first under %q", second.ID, first.ID)
	}
	if second.Points != 0 {
		t.Errorf("the second load wrote %d points, want 0", second.Points)
	}
	after, err := l.Count(ctx, Filter{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if before != after {
		t.Errorf("points went from %d to %d; loading the same result twice must not double anything", before, after)
	}
}

// The other half of the same question, and the one a store gets wrong by being
// helpful: two genuinely different results must not merge. Entity.ID is stable
// within one result and says nothing across runs, so two runs that both call
// something "SuperAI" are two things, and a store that upserted one over the
// other would answer with a graph that never existed.
func TestTwoDifferentResultsAreTwoLoadsAndDoNotMerge(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()

	a := smallResult(8)
	b := smallResult(8)
	b.Entities[0].Name = "SuperAI v2"

	loadA, err := l.Load(ctx, a, LoadOptions{})
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	loadB, err := l.Load(ctx, b, LoadOptions{})
	if err != nil {
		t.Fatalf("load b: %v", err)
	}
	if loadA.ID == loadB.ID {
		t.Fatalf("two different results landed under one load ID %q", loadA.ID)
	}
	if n, err := l.Count(ctx, Filter{Kinds: []string{"entity"}}); err != nil || n != 4 {
		t.Errorf("entities across both loads = %d (err %v), want 4", n, err)
	}
	// And each load still reads as itself, which is the property that makes
	// the merge-refusal useful rather than merely safe.
	recs, err := l.Records(ctx, Filter{Loads: []string{loadB.ID}, Kinds: []string{"entity"}, Type: "Service"}, 0)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(recs.Entities) != 1 || recs.Entities[0].Name != "SuperAI v2" {
		t.Errorf("load B's Service = %+v, want the one named SuperAI v2", recs.Entities)
	}
}

// One name over two different graphs is the caller telling the store two
// things about one import. There is nothing in the data to decide between
// them, so it is refused — and refused before anything is written, because
// half a replacement is worse than either graph.
func TestReusingALoadNameForADifferentResultIsRefused(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, smallResult(8), LoadOptions{ID: "nightly"}); err != nil {
		t.Fatalf("first load: %v", err)
	}
	other := smallResult(8)
	other.Entities[0].Name = "SuperAI v2"

	_, err := l.Load(ctx, other, LoadOptions{ID: "nightly"})
	var ce *ConflictingLoadError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictingLoadError", err)
	}
	if n, err := l.Count(ctx, Filter{Kinds: []string{"entity"}}); err != nil || n != 2 {
		t.Errorf("entities = %d (err %v), want 2: the refused load must have written nothing", n, err)
	}

	// Replace is how a caller says they meant it. The old graph goes, the new
	// one takes the name, and nothing from the first survives to be found by a
	// query scoped to that name.
	if _, err := l.Load(ctx, other, LoadOptions{ID: "nightly", Replace: true}); err != nil {
		t.Fatalf("replacing load: %v", err)
	}
	recs, err := l.Records(ctx, Filter{Loads: []string{"nightly"}, Kinds: []string{"entity"}, Type: "Service"}, 0)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(recs.Entities) != 1 || recs.Entities[0].Name != "SuperAI v2" {
		t.Errorf("after Replace the Service is %+v, want only SuperAI v2", recs.Entities)
	}
}
