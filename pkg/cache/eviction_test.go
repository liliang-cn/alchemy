package cache_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/cache"
)

// keyN is a distinct key per chunk of text, which is how a real job's keys
// differ: same model, same ontology, same prompt, different chunk.
func keyN(n int) cache.Key {
	k := base()
	k.Chunk = fmt.Sprintf("chunk number %d", n)
	return k
}

func entryN(n int) cache.Entry {
	e := storedEntry()
	e.Tokens = n
	return e
}

func mustPut(t *testing.T, c cache.Cache, n int) {
	t.Helper()
	if err := c.Put(context.Background(), keyN(n), entryN(n)); err != nil {
		t.Fatalf("Put %d: %v", n, err)
	}
}

func present(t *testing.T, c cache.Cache, n int) bool {
	t.Helper()
	got, ok, err := c.Get(context.Background(), keyN(n))
	if err != nil {
		t.Fatalf("Get %d: %v", n, err)
	}
	if ok && got.Tokens != n {
		t.Fatalf("Get %d returned entry for %d", n, got.Tokens)
	}
	return ok
}

// TestBoundHolds: NewMemory(n) holds at most n entries. A cache with no bound
// is a memory leak wearing an optimisation's clothes — a long-running node
// importing a large corpus would accumulate every chunk of every job it ever
// coordinated, and §8.4 already refuses work it cannot hold rather than
// accepting it and dying.
func TestBoundHolds(t *testing.T) {
	const max = 4
	c := cache.NewMemory(max)
	for i := 0; i < 50; i++ {
		mustPut(t, c, i)
	}
	live := 0
	for i := 0; i < 50; i++ {
		if present(t, c, i) {
			live++
		}
	}
	if live > max {
		t.Fatalf("cache holds %d entries, bound was %d", live, max)
	}
	if live != max {
		t.Fatalf("cache holds %d entries after 50 puts, want the bound %d filled", live, max)
	}
}

// TestEvictionIsLeastRecentlyUsed: on overflow the entry nobody has touched for
// longest goes. A job walks its chunks roughly in order and a resumed job walks
// them in the same order, so recency is a real predictor of the next hit here,
// which is the argument for LRU over random or FIFO.
func TestEvictionIsLeastRecentlyUsed(t *testing.T) {
	c := cache.NewMemory(3)
	mustPut(t, c, 1)
	mustPut(t, c, 2)
	mustPut(t, c, 3)
	mustPut(t, c, 4) // overflows; 1 is the least recently used

	if present(t, c, 1) {
		t.Errorf("entry 1 survived; it was the least recently used")
	}
	for _, n := range []int{2, 3, 4} {
		if !present(t, c, n) {
			t.Errorf("entry %d was evicted before entry 1", n)
		}
	}
}

// TestGetCountsAsAUse is the claim LRU has to earn. If reads did not refresh
// recency the policy would be insertion-order eviction with an LRU label, and
// the entry a resumed job keeps hitting would be thrown away in favour of one
// written once and never read.
func TestGetCountsAsAUse(t *testing.T) {
	c := cache.NewMemory(3)
	mustPut(t, c, 1)
	mustPut(t, c, 2)
	mustPut(t, c, 3)

	if !present(t, c, 1) { // touch 1, making 2 the least recently used
		t.Fatalf("entry 1 missing before the touch")
	}

	mustPut(t, c, 4)

	if present(t, c, 2) {
		t.Errorf("entry 2 survived; after 1 was read it was the least recently used")
	}
	if !present(t, c, 1) {
		t.Errorf("entry 1 was evicted even though it had just been read — Get did not count as a use")
	}
}

// TestPutOfAnExistingKeyDoesNotGrow: a re-Put of the same address is an
// overwrite, not a second entry. §8.3 has two nodes briefly working the same
// job after a lease expires and says the second writer must lose harmlessly;
// if that write also consumed a slot, at-least-once delivery would evict the
// cache in proportion to how often it happened.
func TestPutOfAnExistingKeyDoesNotGrow(t *testing.T) {
	c := cache.NewMemory(2)
	mustPut(t, c, 1)
	mustPut(t, c, 2)
	mustPut(t, c, 1)
	mustPut(t, c, 1)

	if !present(t, c, 1) || !present(t, c, 2) {
		t.Fatalf("re-putting an existing key evicted the other entry")
	}
}

// TestNonPositiveBoundDisablesTheCache: NewMemory(0) is a cache that stores
// nothing rather than one that stores everything. A zero that means "unbounded"
// is how a config default silently becomes a leak, and this way a caller who
// wants no caching gets a working Cache instead of a nil interface to
// nil-check at every call site.
func TestNonPositiveBoundDisablesTheCache(t *testing.T) {
	for _, max := range []int{0, -1} {
		c := cache.NewMemory(max)
		mustPut(t, c, 1)
		if present(t, c, 1) {
			t.Errorf("NewMemory(%d) stored an entry", max)
		}
	}
}
