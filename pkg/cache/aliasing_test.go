package cache_test

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
)

// The two tests below are the same bug seen from its two sides: a Go slice
// stored by value still points at the caller's backing array, so either party
// can rewrite the other's data. It matters here more than usual. §8.3 has a
// node re-running a job after a lease expiry; if a cached entry can be edited
// in place by whatever the extractor or the merger does next, the resumed job
// produces a different graph from the fresh one, and the difference is
// invisible — the counts still add up and every edge still has provenance.

// TestPutDoesNotAliasTheCallersSlice: the caller keeps using the slice it
// handed over — merging into it, sorting it, retyping an entity after review.
// None of that may reach the stored copy.
func TestPutDoesNotAliasTheCallersSlice(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(8)
	k := base()

	stored := storedEntry()
	if err := c.Put(ctx, k, stored); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Mutate through the caller's own slices, the way a merge or a review edit
	// would: retype an entity, redirect an edge, blank a provenance.
	stored.Entities[0].Type = "MUTATED"
	stored.Entities[1].Provenance.Model = ""
	stored.Relations[0].To = "somewhere-else"
	stored.Relations[0].Provenance.Producer = alchemy.ProducerDDL

	got, ok, err := c.Get(ctx, k)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	assertEntryEqual(t, got, storedEntry())
}

// TestGetDoesNotAliasTheStoredEntry: the caller mutates what it was handed,
// which is the normal thing to do with an extraction result — the merger
// rewrites entity IDs into the job's identity index. The next reader of the
// same address must still get the original.
func TestGetDoesNotAliasTheStoredEntry(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(8)
	k := base()

	if err := c.Put(ctx, k, storedEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	first, _, err := c.Get(ctx, k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	first.Entities[0].Name = "MUTATED"
	first.Entities[0].Provenance.Confidence = 0
	first.Relations[0].Type = "MUTATED"
	if first.Entities[0].Attributes != nil {
		first.Entities[0].Attributes["lang"] = "mutated"
	}

	second, ok, err := c.Get(ctx, k)
	if err != nil || !ok {
		t.Fatalf("second Get: ok=%v err=%v", ok, err)
	}
	assertEntryEqual(t, second, storedEntry())
}
