package tabular

import (
	"context"
	"strings"
	"testing"
)

// A wrong delimiter does not fail: it produces one enormous column, and a table
// read under it runs cleanly and means nothing. That is the same failure shape
// as a wrong mapping, so the sniff is allowed to be certain or to refuse, and
// never to prefer.

func readSniffed(t *testing.T, source, in string, m *Mapping) (Result, error) {
	t.Helper()
	return Read(context.Background(), source, strings.NewReader(in), Options{Mapping: m})
}

var idOnly = &Mapping{EntityType: "Row", IDColumn: "id", Attributes: map[string]string{"city": "city"}}

func TestSniffsTabSemicolonAndPipe(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"tab", "id\tcity\n1\tParis\n2\tBerlin\n"},
		{"semicolon", "id;city\n1;Paris\n2;Berlin\n"},
		{"pipe", "id|city\n1|Paris\n2|Berlin\n"},
		{"comma", "id,city\n1,Paris\n2,Berlin\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := readSniffed(t, "t.csv", tc.in, idOnly)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(res.Entities) != 2 {
				t.Fatalf("entities = %+v", res.Entities)
			}
			if got := res.Entities[0].Attributes["city"]; got != "Paris" {
				t.Errorf("city = %v, want Paris — the delimiter was not the one the file uses", got)
			}
		})
	}
}

// Two delimiters that both parse the file consistently are two readings that
// both run cleanly. Picking one is the §2.1 failure with a different subject.
func TestAmbiguousDelimiterIsRefusedRatherThanPicked(t *testing.T) {
	_, err := readSniffed(t, "t.csv", "id,city;country\n1,Paris;FR\n2,Berlin;DE\n", idOnly)
	if err == nil {
		t.Fatal("want an error: both ',' and ';' parse this file consistently")
	}
	if !strings.Contains(err.Error(), ",") || !strings.Contains(err.Error(), ";") {
		t.Errorf("error = %q, want it to name both candidates", err)
	}
}

// A file with no delimiter in it at all is not ambiguous: every candidate reads
// it as the same single column, so there is nothing to be wrong about.
func TestSingleColumnFileNeedsNoDelimiter(t *testing.T) {
	res, err := readSniffed(t, "t.csv", "id\n1\n2\n", &Mapping{EntityType: "Row", IDColumn: "id"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 2 || res.Entities[1].ID != "row:2" {
		t.Fatalf("entities = %+v", res.Entities)
	}
}

// A candidate that separates the header but not the rows has not been found; it
// has been mistaken for one. Refuse rather than emit one enormous column.
func TestInconsistentDelimiterIsRefused(t *testing.T) {
	_, err := readSniffed(t, "t.csv", "id;city;country\n1;Paris\n2;Berlin;DE;extra\n", idOnly)
	if err == nil {
		t.Fatal("want an error: ';' does not parse this file consistently")
	}
}

// A quoted field may contain a newline. Counting physical lines makes such a
// file look inconsistent under every candidate, and the reader would then
// refuse a table that is perfectly well formed — a refusal is cheap to notice,
// but a reader that refuses legal CSV is a reader nobody uses.
func TestAQuotedNewlineDoesNotDefeatTheSniff(t *testing.T) {
	in := "id,note\n1,\"first line\nsecond line\"\n2,plain\n"
	res, err := readSniffed(t, "t.csv", in, &Mapping{
		EntityType: "Row", IDColumn: "id", Attributes: map[string]string{"note": "note"},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %+v", res.Entities)
	}
	if got := res.Entities[0].Attributes["note"]; got != "first line\nsecond line" {
		t.Errorf("note = %q, want both lines of the quoted field", got)
	}
}
