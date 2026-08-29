package cache

import "context"

// Fetch returns the cached entry for k, or calls produce and stores what it
// returns. The bool reports whether the answer came from the cache.
//
// It exists so the "a cache failure is not a job failure" contract is written
// once. Every call site could implement it — check, call on miss, store — and
// every call site would then have to decide independently what to do with an
// error from Get, which is exactly the kind of decision that gets made three
// different ways in three files and wrong in one of them.
//
// The rules, and why:
//
//   - A cache error is treated as a miss. §8.2 added the cache so a resumed job
//     does not re-buy work, not so an unreachable store could kill an import;
//     the worst a broken cache may do is make the job cost what it would have
//     cost without one. The error is not returned, because a caller that cannot
//     act on it would only log it, and a caller that can is holding the Cache
//     and can instrument its own implementation.
//   - A nil Cache means caching is off. The alternative is a nil check at every
//     call site, and the one that is forgotten panics an import.
//   - A producer error is returned and nothing is stored. Caching a failed
//     extraction would make the failure permanent for that content address —
//     the one bug worse than paying for the call twice.
//   - A failed Put is swallowed for the same reason as a failed Get: a store
//     that could not accept the answer has not invalidated the answer.
func Fetch(ctx context.Context, c Cache, k Key, produce func(context.Context) (Entry, error)) (Entry, bool, error) {
	if c != nil {
		if e, ok, err := c.Get(ctx, k); err == nil && ok {
			return e, true, nil
		}
	}

	e, err := produce(ctx)
	if err != nil {
		return Entry{}, false, err
	}
	if c != nil {
		// Deliberately ignored: see the contract above.
		_ = c.Put(ctx, k, e)
	}
	return e, false, nil
}
