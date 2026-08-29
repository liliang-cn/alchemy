package cache_test

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/cache"
)

// brokenCache is the shared store of §8.3 with the network cut: every call
// fails. It exists to prove the failure mode is degraded speed, not a dead job.
type brokenCache struct{ gets, puts int }

var errBroken = errors.New("cache store unreachable")

func (b *brokenCache) Get(ctx context.Context, k cache.Key) (cache.Entry, bool, error) {
	b.gets++
	return cache.Entry{}, false, errBroken
}

func (b *brokenCache) Put(ctx context.Context, k cache.Key, e cache.Entry) error {
	b.puts++
	return errBroken
}

// TestABrokenCacheDoesNotBreakTheWork. A cache is an optimisation, and an
// optimisation that can fail a job is worse than no cache: §8.2 added this to
// avoid re-buying a call, not to add a new way for an import to die. So a store
// that errors on every call still yields the extraction, from the producer.
func TestABrokenCacheDoesNotBreakTheWork(t *testing.T) {
	broken := &brokenCache{}
	called := 0

	got, hit, err := cache.Fetch(context.Background(), broken, base(), func(ctx context.Context) (cache.Entry, error) {
		called++
		return storedEntry(), nil
	})
	if err != nil {
		t.Fatalf("Fetch through a broken cache failed the work: %v", err)
	}
	if hit {
		t.Errorf("Fetch reported a hit from a cache that errored")
	}
	if called != 1 {
		t.Fatalf("producer called %d times, want 1", called)
	}
	assertEntryEqual(t, got, storedEntry())

	// The failed Put is also swallowed: a store that cannot accept the answer
	// has not invalidated the answer.
	if broken.puts != 1 {
		t.Errorf("Fetch made %d Put attempts, want 1", broken.puts)
	}
}

// TestFetchBuysTheCallOnceAndThenStopsBuying is the requirement in one
// sentence: a resumed job does not re-buy what it already has.
func TestFetchBuysTheCallOnceAndThenStopsBuying(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(8)
	called := 0
	produce := func(ctx context.Context) (cache.Entry, error) {
		called++
		return storedEntry(), nil
	}

	if _, hit, err := cache.Fetch(ctx, c, base(), produce); err != nil || hit {
		t.Fatalf("first Fetch: hit=%v err=%v", hit, err)
	}
	got, hit, err := cache.Fetch(ctx, c, base(), produce)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !hit {
		t.Fatalf("second Fetch missed a key the first one stored")
	}
	if called != 1 {
		t.Fatalf("producer called %d times, want 1 — the second call was re-bought", called)
	}
	// The cached copy is as attributable as the fresh one.
	assertEntryEqual(t, got, storedEntry())
}

// TestFetchReturnsTheProducersError. A failure to produce is the caller's
// problem and must not be hidden as a miss, and nothing is stored: caching a
// failed extraction would make the failure permanent for that content address,
// which is the one bug worse than re-buying the call.
func TestFetchReturnsTheProducersError(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(8)
	boom := errors.New("model returned 429")

	_, _, err := cache.Fetch(ctx, c, base(), func(ctx context.Context) (cache.Entry, error) {
		return cache.Entry{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Fetch swallowed the producer's error: %v", err)
	}
	if _, ok, _ := c.Get(ctx, base()); ok {
		t.Fatalf("a failed extraction was cached")
	}
}

// TestFetchWithoutACacheStillWorks: a nil Cache is "caching off", not a panic.
// The alternative is every call site nil-checking, and one that forgets
// crashes an import.
func TestFetchWithoutACacheStillWorks(t *testing.T) {
	got, hit, err := cache.Fetch(context.Background(), nil, base(), func(ctx context.Context) (cache.Entry, error) {
		return storedEntry(), nil
	})
	if err != nil || hit {
		t.Fatalf("Fetch with a nil cache: hit=%v err=%v", hit, err)
	}
	assertEntryEqual(t, got, storedEntry())
}
