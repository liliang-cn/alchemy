package cache

import (
	"container/list"
	"context"
	"sync"
)

// memory is the single-node cache: the default a buyer evaluating the product
// runs (§8.3), and what the shared store is an alternative to rather than a
// replacement for.
//
// The policy is least-recently-used. The argument is the access pattern §8.2
// describes: a coordinator walks a job's chunks, and a node that picks the job
// up after a lease expiry walks the same chunks in the same order, so the
// entries worth keeping are the ones touched most recently — recency here is a
// real predictor rather than a folk default. FIFO would be almost as good on
// that pattern alone and much worse when several jobs interleave on one node,
// because it discards an entry a live job is still reading in favour of one
// written once by a job that has finished. Random eviction needs no bookkeeping
// but makes the bad case unbounded and unreproducible, and "the resumed job was
// slower this time and nobody can say why" is a support ticket with no end.
//
// The cost of LRU is that a read is a write: Get moves an element to the front,
// so it takes the same lock as Put and cannot be an RWMutex read. That is
// accepted because the work behind a miss is a network call to a model
// endpoint (§8.2 — the bottleneck is not our CPU), and a mutex hold long enough
// to unlink and relink a list node is not what limits a node's throughput.
type memory struct {
	max int

	mu sync.Mutex
	// order is most-recently-used at the front. Its elements hold *record, and
	// entries indexes into it by address so both a lookup and a recency update
	// are O(1) — §8.1 rejects the O(n²) merge for the same reason.
	order   *list.List
	entries map[string]*list.Element
}

// record is what a list element carries: the address is kept alongside the
// value so eviction can delete the map key without searching for it.
type record struct {
	address string
	entry   Entry
}

// NewMemory returns an in-process cache holding at most maxEntries entries,
// evicting the least recently used.
//
// maxEntries <= 0 returns a cache that stores nothing. Making zero mean
// "unbounded" is how a config default becomes a leak; making it mean "off"
// gives a caller who does not want caching a working Cache rather than a nil
// interface to check for at every call site.
func NewMemory(maxEntries int) Cache {
	return &memory{
		max:     maxEntries,
		order:   list.New(),
		entries: make(map[string]*list.Element),
	}
}

func (m *memory) Get(ctx context.Context, k Key) (Entry, bool, error) {
	address := k.Address()

	m.mu.Lock()
	defer m.mu.Unlock()

	el, ok := m.entries[address]
	if !ok {
		// A miss is not an error: the work simply has not been done yet.
		return Entry{}, false, nil
	}
	m.order.MoveToFront(el)
	return clone(el.Value.(*record).entry), true, nil
}

func (m *memory) Put(ctx context.Context, k Key, e Entry) error {
	// Checked even though this store never serialises anything. The domain is
	// the Cache contract's, not the shared store's, and a domain that only the
	// clustered deployment enforces is one a single-node run would teach every
	// caller to violate — see ErrUnsupportedAttribute.
	if err := validate(e); err != nil {
		return err
	}
	if m.max <= 0 {
		return nil
	}
	address := k.Address()
	// Clone outside the lock: it is the only part of Put that is proportional
	// to the size of an extraction result.
	stored := clone(e)

	m.mu.Lock()
	defer m.mu.Unlock()

	if el, ok := m.entries[address]; ok {
		// Same address means the same answer to the same question, so this is
		// an overwrite and must not consume a second slot — §8.3 has two nodes
		// briefly working one job after a lease expires, and the second writer
		// is required to lose harmlessly.
		el.Value.(*record).entry = stored
		m.order.MoveToFront(el)
		return nil
	}

	m.entries[address] = m.order.PushFront(&record{address: address, entry: stored})
	for m.order.Len() > m.max {
		oldest := m.order.Back()
		m.order.Remove(oldest)
		delete(m.entries, oldest.Value.(*record).address)
	}
	return nil
}
