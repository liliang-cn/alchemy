package qdrant

import (
	"context"
	"errors"
	"testing"
	"time"
)

// §8.4: a big result is not one message. Here it is not one request either,
// and the batching has to be real rather than a constant nobody exercises —
// so this loads more than one batch's worth and checks that all of it arrived.
func TestALargeResultIsManyRequestsAndArrivesWhole(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{Batch: 100})
	ctx := context.Background()
	res := bigResult(450, 8)

	got, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Batches < 12 {
		t.Errorf("Batches = %d, want at least 12 for 1349 points at 100 a request", got.Batches)
	}
	for _, tc := range []struct {
		kind string
		want int
	}{{"entity", 450}, {"chunk", 450}, {"relation", 449}} {
		n, err := l.Count(ctx, Filter{Kinds: []string{tc.kind}})
		if err != nil {
			t.Fatalf("count %s: %v", tc.kind, err)
		}
		if n != tc.want {
			t.Errorf("%s points = %d, want %d", tc.kind, n, tc.want)
		}
	}
	// The search still finds the one chunk it should, which is the property
	// that would quietly break if a batch had been dropped.
	hits, err := l.Search(ctx, unit(8, 3), 1, Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk%8 != 3 {
		t.Errorf("nearest chunk = %+v, want one embedded on axis 3", hits)
	}
}

// The outcome this whole arrangement is built to avoid is a half-loaded
// collection nobody can tell is half-loaded. A load that dies between batches
// is an ordinary event at volume, so it is broken on purpose here: what it
// leaves must be invisible to every query and plainly visible to an operator.
func TestALoadThatDiesHalfwayIsInvisibleAndVisiblyIncomplete(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{Batch: 50})
	ctx := context.Background()
	boom := errors.New("the network went away")
	// The batch counter moved. upsert's `written` used to be cumulative over a
	// whole result; the envelope (pkg/sink) now hands this store one batch at a
	// time, which is the point of it — a large graph reaches a store as a
	// stream and never as a struct — so the running total is kept here. The
	// failure lands where it always did: after 100 entity points are in the
	// collection and before the rest.
	landed := 0
	l.hooks.afterBatch = func(kind string, written int) error {
		if kind != "entity" {
			return nil
		}
		landed += written
		if landed >= 100 {
			return boom
		}
		return nil
	}

	got, err := l.Load(ctx, bigResult(300, 8), LoadOptions{ID: "nightly"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if got.Points == 0 {
		t.Fatal("the failed load reported no points; it should say how much of it arrived")
	}
	if n := f.pointsOf(t, l, "nightly"); n < 100 {
		t.Fatalf("the collection holds %d points of the dead load, want the ones that landed before it died", n)
	}

	// Invisible: every read excludes it, because its marker never said
	// complete.
	if n, err := l.Count(ctx, Filter{}); err != nil || n != 0 {
		t.Errorf("Count over a half-loaded collection = %d (err %v), want 0", n, err)
	}
	if hits, err := l.Search(ctx, unit(8, 1), 5, Filter{}); err != nil || len(hits) != 0 {
		t.Errorf("Search over a half-loaded collection = %d hits (err %v), want 0", len(hits), err)
	}
	// Visible: an operator asking what is in the store sees exactly the load
	// that stopped.
	loads, err := l.Loads(ctx)
	if err != nil || len(loads) != 1 {
		t.Fatalf("Loads = %+v (err %v), want the incomplete one", loads, err)
	}
	if loads[0].ID != "nightly" || loads[0].Complete {
		t.Errorf("Loads = %+v, want nightly, incomplete", loads[0])
	}

	// And it can be cleaned up, by name or by age, without touching anything
	// else.
	if _, err := l.Sweep(ctx, 0); err == nil {
		t.Error("Sweep with a zero cutoff was accepted; it would remove loads that are still running")
	}
	swept, err := l.Sweep(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(swept.Abandoned) != 1 || swept.Abandoned[0] != "nightly" {
		t.Errorf("swept %v, want [nightly]", swept.Abandoned)
	}
	if n := f.pointsOf(t, l, "nightly"); n != 0 {
		t.Errorf("%d points survived the sweep, want 0", n)
	}

	// The name is free again, and the retry is a load like any other.
	l.hooks.afterBatch = nil
	if _, err := l.Load(ctx, bigResult(300, 8), LoadOptions{ID: "nightly"}); err != nil {
		t.Fatalf("reloading after the sweep: %v", err)
	}
	if n, err := l.Count(ctx, Filter{Kinds: []string{"entity"}}); err != nil || n != 300 {
		t.Errorf("entities after the retry = %d (err %v), want 300", n, err)
	}
}

// A retry that does not wait for a sweep is the commoner case: the operator
// runs the same command again. The name is taken by a load that never
// finished, so the connector says so and says what to do about it — and
// Replace does it.
func TestRetryingADeadLoadUnderTheSameNameNeedsReplace(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{Batch: 50})
	ctx := context.Background()
	boom := errors.New("the network went away")
	l.hooks.afterBatch = func(kind string, written int) error {
		if kind == "entity" && written >= 50 {
			return boom
		}
		return nil
	}
	res := bigResult(120, 8)
	if _, err := l.Load(ctx, res, LoadOptions{ID: "nightly"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}

	l.hooks.afterBatch = nil
	_, err := l.Load(ctx, res, LoadOptions{ID: "nightly"})
	var ce *ConflictingLoadError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictingLoadError naming the dead load", err)
	}
	if ce.Complete {
		t.Error("the refusal says the load is complete; it died halfway")
	}
	if _, err := l.Load(ctx, res, LoadOptions{ID: "nightly", Replace: true}); err != nil {
		t.Fatalf("replacing the dead load: %v", err)
	}
	if n, err := l.Count(ctx, Filter{Kinds: []string{"entity"}}); err != nil || n != 120 {
		t.Errorf("entities = %d (err %v), want 120 exactly: the dead load's points must not survive beside the retry", n, err)
	}
}
