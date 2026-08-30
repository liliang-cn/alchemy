package pgvector

import (
	"context"
	"testing"
)

// pgvector's own Search refuses k <= 0 for the same reason: an unbounded
// anchor search over a four-hundred-thousand-record import is a page nobody
// reads and a query nobody meant. There is no "everything" value on purpose,
// and the refusal comes before the pool is touched so a caller gets their own
// mistake back rather than the database's.
func TestAnAnchorSearchWithoutALimitIsRefusedRatherThanUnbounded(t *testing.T) {
	l, err := New(nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, limit := range []int{0, -1} {
		if _, err := l.Find(context.Background(), "load", "ravel", limit); err == nil {
			t.Errorf("Find(limit %d) was accepted", limit)
		}
	}
}
