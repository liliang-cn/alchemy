package cache_test

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/cache"
)

// TestConcurrentGetPutOnOverlappingKeys. §8.1 makes the chunk the unit of
// parallelism: many goroutines on one node extract chunks of the same job at
// once, against one cache, and after a lease expiry (§8.3) two nodes work
// overlapping chunk sets — so overlapping keys under concurrency is the normal
// case, not the stress case. Run with -race.
func TestConcurrentGetPutOnOverlappingKeys(t *testing.T) {
	const (
		workers  = 32
		rounds   = 200
		distinct = 16 // fewer keys than workers, so they collide on purpose
		bound    = 8  // smaller than distinct, so eviction runs under contention
	)
	ctx := context.Background()
	c := cache.NewMemory(bound)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				n := (w + i) % distinct
				// Every worker both reads and writes the same addresses, and
				// mutates what it gets back — the aliasing bug and the data
				// race are the same bug seen at two scales, so the test that
				// hunts one should provoke the other.
				got, ok, err := c.Get(ctx, keyN(n))
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				if ok {
					if got.Tokens != n {
						t.Errorf("address for %d returned entry for %d", n, got.Tokens)
						return
					}
					got.Entities[0].Name = "mutated by the caller"
					got.Relations[0].Provenance.Model = ""
				}
				if err := c.Put(ctx, keyN(n), entryN(n)); err != nil {
					t.Errorf("Put: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// The bound still holds after all that contention: eviction under a race is
	// where an LRU implementation leaks map keys it forgot to delete.
	live := 0
	for n := 0; n < distinct; n++ {
		if present(t, c, n) {
			live++
		}
	}
	if live > bound {
		t.Fatalf("cache holds %d entries after concurrent use, bound was %d", live, bound)
	}
}

// TestConcurrentFetch: the same, through Fetch, where the producer is the
// expensive model call. Two workers may both miss and both produce — §8.3
// accepts at-least-once and requires the second writer to lose harmlessly —
// but neither may see a torn entry.
func TestConcurrentFetch(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(4)

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				n := (w + i) % 6
				got, _, err := cache.Fetch(ctx, c, keyN(n), func(ctx context.Context) (cache.Entry, error) {
					return entryN(n), nil
				})
				if err != nil {
					t.Errorf("Fetch: %v", err)
					return
				}
				if got.Tokens != n || len(got.Entities) != 2 || got.Entities[0].Provenance.Model == "" {
					t.Errorf("Fetch returned a torn entry for %d: %+v", n, got)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
