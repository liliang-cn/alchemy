package tabular

import (
	"context"
	"io"
	"runtime"
	"testing"
)

// generated is a table produced on demand, so the test's own source is never in
// memory either. It records how it was read.
type generated struct {
	header  string
	row     string
	rows    int
	emitted int
	off     int
	buf     string

	reads    int
	maxAsked int
	total    int
}

func (g *generated) Read(p []byte) (int, error) {
	if g.maxAsked < len(p) {
		g.maxAsked = len(p)
	}
	g.reads++
	if g.off >= len(g.buf) {
		if g.emitted == 0 {
			g.buf, g.off = g.header, 0
		} else if g.emitted <= g.rows {
			g.buf, g.off = g.row, 0
		} else {
			return 0, io.EOF
		}
		g.emitted++
	}
	n := copy(p, g.buf[g.off:])
	g.off += n
	g.total += n
	return n, nil
}

// DESIGN.md §8.4: a big source is not held in memory. The reader is asked for
// the source in bounded pieces — never in one — and a table far larger than any
// buffer in this package is read to the end.
func TestALargeSourceIsReadInBoundedPieces(t *testing.T) {
	const rows = 400_000
	g := &generated{header: "id,city\n", row: "1,Paris\n", rows: rows}
	res, err := Read(context.Background(), "big.csv", g, Options{Delimiter: ',', Mapping: idCity})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if g.total < rows*8 {
		t.Fatalf("read %d bytes, want the whole table", g.total)
	}
	if g.maxAsked > sniffWindow {
		t.Errorf("largest single Read was %d bytes, want no more than the %d-byte window: "+
			"a source asked for whole is a source held whole", g.maxAsked, sniffWindow)
	}
	if g.reads < rows/8 {
		t.Errorf("the source was read in %d calls, want it consumed incrementally", g.reads)
	}
	// Every row carries the same identity and the same values, so they collapse:
	// what is left in memory at the end is one entity, not four hundred thousand.
	if len(res.Entities) != 1 || len(res.Violations) != 0 {
		t.Fatalf("entities = %d, violations = %+v", len(res.Entities), res.Violations)
	}
}

// The same table again, this time watching the heap: a reader that held the
// source would end holding megabytes of it.
func TestReadingALargeTableDoesNotGrowTheHeapWithIt(t *testing.T) {
	const rows = 1_000_000 // ~8MB of CSV
	g := &generated{header: "id,city\n", row: "1,Paris\n", rows: rows}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, err := Read(context.Background(), "big.csv", g, Options{Delimiter: ',', Mapping: idCity})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew > 2<<20 {
		t.Errorf("the heap grew by %d bytes reading %d bytes of source, want the source streamed", grew, g.total)
	}
	runtime.KeepAlive(res)
}
