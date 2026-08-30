package tabular

import "hash/fnv"

// ids remembers what has been emitted, so a second row claiming an identity
// already taken is noticed.
//
// It holds a 64-bit fingerprint rather than the row, because a table with ten
// million rows is a table whose duplicate check must not cost a second copy of
// it (§8.4). A fingerprint collision would report a duplicate as identical when
// it differs, which loses a violation and never invents one.
type ids struct {
	seen map[string]entry
}

type entry struct {
	line int
	sum  uint64
}

func newIDs() *ids { return &ids{seen: map[string]entry{}} }

// add records an id. It reports the line that claimed it first, whether that
// row produced something different, and whether this is a duplicate at all.
//
// Rows that produce the same entity collapse without a word: a re-exported file
// repeating a row loses nothing when the repeat is dropped, and neither does one
// whose only difference is in a column the mapping never reads — reporting that
// would fill the review queue (§5c) with items a reviewer cannot act on. Rows
// that produce different entities are reported: which of them is right is not in
// the data, and the first one wins only because two entities sharing an id break
// every consumer that walks the graph by id. pkg/source/ddl resolves the same
// situation the same way.
func (s *ids) add(id string, line int, sum uint64) (first int, differs, dup bool) {
	if e, ok := s.seen[id]; ok {
		return e.line, e.sum != sum, true
	}
	s.seen[id] = entry{line: line, sum: sum}
	return 0, false, false
}

// claimed reports whether a row already produced this entity. It is what keeps
// a referenced entity from displacing the row that describes it: the stub knows
// an id and nothing else, and a table that mentions node-a two rows before it
// describes it must still come back with the description.
func (s *ids) claimed(id string) bool {
	_, ok := s.seen[id]
	return ok
}

// fingerprint covers what the mapping reads, which is what reaches the graph.
func fingerprint(values []string) uint64 {
	h := fnv.New64a()
	for _, v := range values {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}
