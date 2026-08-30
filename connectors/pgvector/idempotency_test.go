package pgvector

import (
	"context"
	"errors"
	"testing"
)

// §5 puts "incremental re-import and change detection" in the second release,
// so this release has to decide what loading the same thing twice does. It must
// not double the data and must not merge two runs, and Entity.ID cannot help
// with either: it is stable within one result and says nothing across runs.
// The whole result is therefore the unit of identity.
func TestLoadingTheSameResultTwiceWritesNothingTheSecondTime(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	res := smallResult(8)

	first, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if first.Already {
		t.Fatal("the first load reported that it had already happened")
	}
	second, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !second.Already {
		t.Error("the second load of an identical result did not report Already")
	}
	if second.ID != first.ID {
		t.Errorf("second load ID = %q, want %q: a replay belongs to the load it replays", second.ID, first.ID)
	}
	if n := f.count(t, "entities"); n != 2 {
		t.Errorf("entities = %d, want 2: the graph was doubled", n)
	}
	if n := f.count(t, "loads"); n != 1 {
		t.Errorf("loads = %d, want 1", n)
	}
}

// Two genuinely different runs are two graphs and stay two graphs. Merging them
// on entity ID would be the store answering a question the data does not: the
// same corpus re-extracted under a different chunking strategy produces the
// same IDs for a different graph (§7.1).
func TestTwoDifferentResultsAreTwoLoadsAndAreNotMerged(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()

	a := smallResult(8)
	b := smallResult(8)
	b.Entities[0].Name = "SuperAI (v2)" // same ID, different claim about the world
	first, err := l.Load(ctx, a, LoadOptions{})
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	second, err := l.Load(ctx, b, LoadOptions{})
	if err != nil {
		t.Fatalf("load b: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("two different graphs landed under one ID")
	}
	if n := f.count(t, "loads"); n != 2 {
		t.Errorf("loads = %d, want 2", n)
	}
	if n := f.count(t, "entities"); n != 4 {
		t.Errorf("entities = %d, want 4: each load keeps its own SuperAI", n)
	}
	ga, err := l.Graph(ctx, first.ID)
	if err != nil {
		t.Fatalf("graph a: %v", err)
	}
	for _, e := range ga.Entities {
		if e.ID == "SuperAI" && e.Name != "SuperAI" {
			t.Errorf("load %s sees %q; the second run overwrote the first", first.ID, e.Name)
		}
	}
}

// A caller who names a load owns the name. Reusing it for a different graph is
// refused rather than merged or overwritten, because there is no key on which
// the two could be joined and picking one silently is the store deciding.
func TestReusingALoadIDForADifferentGraphIsRefused(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, smallResult(8), LoadOptions{ID: "nightly"}); err != nil {
		t.Fatalf("first load: %v", err)
	}
	other := smallResult(8)
	other.Entities[0].Name = "SuperAI (v2)"

	_, err := l.Load(ctx, other, LoadOptions{ID: "nightly"})
	var ce *ConflictingLoadError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictingLoadError", err)
	}
	if ce.ID != "nightly" || ce.Have == ce.Want {
		t.Errorf("ConflictingLoadError = %+v, want it to name nightly and two fingerprints", ce)
	}
	if n := f.count(t, "entities"); n != 2 {
		t.Errorf("entities = %d, want 2: the refused load wrote rows", n)
	}

	// Replace is how the caller says they meant it. The old graph goes, whole.
	out, err := l.Load(ctx, other, LoadOptions{ID: "nightly", Replace: true})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if out.Already {
		t.Error("a replace reported Already")
	}
	if n := f.count(t, "entities"); n != 2 {
		t.Errorf("entities = %d after replace, want 2", n)
	}
	g, err := l.Graph(ctx, "nightly")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	for _, e := range g.Entities {
		if e.ID == "SuperAI" && e.Name != "SuperAI (v2)" {
			t.Errorf("after replace SuperAI is %q, want the new graph's name", e.Name)
		}
	}
}

// Replaying a named load with the identical graph is the retry case, and a
// retry that does nothing is a success rather than a collision.
func TestReplayingANamedLoadIsANoOp(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	if _, err := l.Load(ctx, res, LoadOptions{ID: "nightly"}); err != nil {
		t.Fatalf("first load: %v", err)
	}
	out, err := l.Load(ctx, res, LoadOptions{ID: "nightly"})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !out.Already || out.ID != "nightly" {
		t.Errorf("replay = %+v, want Already under nightly", out)
	}
	if n := f.count(t, "entities"); n != 2 {
		t.Errorf("entities = %d, want 2", n)
	}
}

// A result whose own entity IDs collide is refused, because relations refer to
// entities by ID and either winner makes some edge point at the wrong node.
func TestCollidingEntityIDsWithinOneResultAreRefused(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Entities[1].ID = "SuperAI"
	_, err := l.Load(context.Background(), res, LoadOptions{})
	var de *DuplicateEntityError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DuplicateEntityError", err)
	}
	if n := f.count(t, "loads"); n != 0 {
		t.Errorf("loads = %d, want 0", n)
	}
}
