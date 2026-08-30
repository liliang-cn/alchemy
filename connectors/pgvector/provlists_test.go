package pgvector

import (
	"reflect"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// TestTheFourProvenanceListsCoverEveryField holds provCols, provNames, provRow
// and provDest to the shape of alchemy.Provenance itself.
//
// Four, not three. The comment on provDest said three, and the fourth —
// provNames, the column list COPY uses — is the one that actually broke: By
// and At reached provCols, provRow and provDest and not it, so every write of
// five hundred entities failed with "extra data after last expected column"
// while every read compiled and every unit test passed.
//
// The comment on provDest used to say that adding a field to that struct would
// break the build in all three lists. It never could: they are a string, a
// []any and a []any, and a list one element short compiles. Provenance gained
// By and At and all three went on compiling while writing neither — a store
// that had kept the producer and lost the person who asserted it, which is the
// §5b guarantee inverted and exactly the failure nothing would have reported.
//
// prov_deterministic is the one column with no field behind it: it is computed
// by provRow from the producer rather than read off the struct, which is why
// the column count is one more than the field count and why that difference is
// asserted here rather than tolerated as slack. If it ever stops being exactly
// one, either a field is missing from the columns or a column has appeared
// that nothing in Provenance accounts for.
func TestTheFourProvenanceListsCoverEveryField(t *testing.T) {
	fields := reflect.TypeOf(alchemy.Provenance{}).NumField()

	// provNames is checked too, and it is the one that mattered: provCols is a
	// SELECT projection and provNames is what COPY names its columns, so a
	// provNames that is short does not fail a query — it fails a write, with
	// PostgreSQL's "extra data after last expected column", which is what the
	// database said when By and At were added to three of the four lists.
	if got, want := len(provNames), fields+1; got != want {
		t.Errorf("provNames lists %d columns, want %d: a COPY into a table whose column list "+
			"is short of the row it is given fails at run time, not at compile time\n  %v",
			got, want, provNames)
	}

	cols := strings.Split(provCols, ",")
	if got, want := len(cols), fields+1; got != want {
		t.Errorf("provCols names %d columns and alchemy.Provenance has %d fields (+1 for the "+
			"computed prov_deterministic), want %d\n  %s", got, fields, want, provCols)
	}
	if got, want := len(provRow(alchemy.Provenance{})), fields+1; got != want {
		t.Errorf("provRow writes %d values, want %d: a field added to alchemy.Provenance is "+
			"not being written", got, want)
	}
	if got, want := len(provDest(&alchemy.Provenance{})), fields+1; got != want {
		t.Errorf("provDest scans %d values, want %d: a field added to alchemy.Provenance is "+
			"not being read back", got, want)
	}
}

// TestProvenanceSurvivesTheRoundTripFieldForField writes every field with a
// distinct value and reads it back, so a transposition is caught.
//
// Two lists of the same length in the wrong order compile, run, and quietly
// write each value into the other's column — the failure the arity check above
// cannot see. Distinct values are what makes it visible: if By and At were
// swapped, both would still be non-empty and only their contents would say so.
func TestProvenanceSurvivesTheRoundTripFieldForField(t *testing.T) {
	want := alchemy.Provenance{
		Source: "s", Chunk: 7, Producer: alchemy.ProducerHuman, Model: "m",
		Ontology: "o", Chunking: "c", Confidence: 0.5, ReviewedBy: "rb",
		RuleSet: "rs", RuledBy: "rd", By: "liliang", At: "2026-08-30T00:00:00Z",
	}
	row := provRow(want)

	var got alchemy.Provenance
	dest := provDest(&got)
	if len(row) != len(dest) {
		t.Fatalf("provRow writes %d and provDest reads %d", len(row), len(dest))
	}
	// The scan a driver would do, without a driver: assign each written value
	// into the target the same position points at.
	for i := range row {
		d := reflect.ValueOf(dest[i])
		if d.Kind() != reflect.Ptr {
			continue
		}
		v := reflect.ValueOf(row[i])
		// Convertible rather than assignable: provRow writes string(p.Producer)
		// and provDest scans into an alchemy.Producer, which pgx reconciles
		// because both are text at the wire. Requiring assignability here would
		// fail on a pair the database handles correctly, and what this test is
		// for is the pairs it does not — a value landing in the wrong column.
		if !v.Type().ConvertibleTo(d.Elem().Type()) {
			t.Fatalf("column %d: provRow writes %s and provDest reads into %s",
				i, v.Type(), d.Elem().Type())
		}
		d.Elem().Set(v.Convert(d.Elem().Type()))
	}
	if got != want {
		t.Errorf("provenance did not survive the round trip\n got %+v\nwant %+v", got, want)
	}
}
