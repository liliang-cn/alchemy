package tabular

import (
	"context"
	"strings"
	"testing"
)

func readFixed(t *testing.T, in string, m *Mapping) (Result, error) {
	t.Helper()
	return Read(context.Background(), "t.csv", strings.NewReader(in), Options{Delimiter: ',', Mapping: m})
}

var idCity = &Mapping{EntityType: "Row", IDColumn: "id", Attributes: map[string]string{"city": "city"}}

// A row with the wrong number of fields cannot be read against the header: the
// cell under "city" is whatever the shift left there. Skipping it is right;
// skipping it quietly is the bug class this package exists to prevent, so the
// violation names the line and what it expected.
func TestWrongFieldCountIsSkippedAndReported(t *testing.T) {
	res, err := readFixed(t, "id,city\n1,Paris\n2\n3,Berlin,extra\n4,Rome\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %+v, want the two well-formed rows", res.Entities)
	}
	if len(res.Violations) != 2 {
		t.Fatalf("violations = %+v", res.Violations)
	}
	for _, v := range res.Violations {
		if v.Kind != ViolationMalformedRow {
			t.Errorf("Kind = %q", v.Kind)
		}
		if !strings.Contains(v.Detail, "2 field") {
			t.Errorf("Detail = %q, want it to say how many fields the header has", v.Detail)
		}
	}
	if !strings.Contains(res.Violations[0].Subject, "line 3") || !strings.Contains(res.Violations[1].Subject, "line 4") {
		t.Errorf("subjects = %q / %q, want each to name its line", res.Violations[0].Subject, res.Violations[1].Subject)
	}
}

// After an unclosed quote every following row boundary is fiction, so continuing
// would swallow an unknown number of rows. pkg/source/ddl refuses an
// unterminated literal for the same reason.
func TestUnclosedQuoteIsAnError(t *testing.T) {
	_, err := readFixed(t, "id,city\n1,\"Paris\n2,Berlin\n3,Rome\n", idCity)
	if err == nil {
		t.Fatal("want an error for an unclosed quote")
	}
	if !strings.Contains(err.Error(), "t.csv") {
		t.Errorf("error = %q, want it to name the source", err)
	}
}

// A byte-order mark is an encoding artefact, not data. Left in place it makes
// the first header cell "\ufeffid", which matches no mapping — a wrong mapping
// arrived at by punctuation. Nothing is lost by removing it, so nothing is said.
func TestBOMIsStrippedSilently(t *testing.T) {
	res, err := readFixed(t, "\ufeffid,city\n1,Paris\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none", res.Violations)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "row:1" {
		t.Fatalf("entities = %+v", res.Entities)
	}
}

func TestCRLFLeavesNoCarriageReturnInValues(t *testing.T) {
	res, err := readFixed(t, "id,city\r\n1,Paris\r\n2,Berlin\r\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %+v", res.Entities)
	}
	if got := res.Entities[0].Attributes["city"]; got != "Paris" {
		t.Errorf("city = %q, want Paris with no carriage return", got)
	}
}

// Two columns of the same name make every mapping that names it undecidable,
// and deciding it by position is precisely §2.1's failure. Refuse the table.
func TestDuplicateHeaderNameIsAnError(t *testing.T) {
	_, err := readFixed(t, "id,city,id\n1,Paris,2\n", idCity)
	if err == nil {
		t.Fatal("want an error for a duplicated header name")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error = %q, want it to name the duplicated column", err)
	}
}

// An unnamed column is unreferenceable rather than ambiguous: no mapping can
// name it, so it is left out — and said so, because a dropped column nobody
// hears about is the same bug as a dropped row.
func TestEmptyHeaderCellIsReportedAndTheColumnIsLeftOut(t *testing.T) {
	res, err := readFixed(t, "id,,city\n1,junk,Paris\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].Kind != ViolationUnnamedColumn {
		t.Fatalf("violations = %+v", res.Violations)
	}
	if !strings.Contains(res.Violations[0].Subject, "column 2") {
		t.Errorf("Subject = %q, want it to locate the column", res.Violations[0].Subject)
	}
	if len(res.Entities) != 1 || res.Entities[0].Attributes["city"] != "Paris" {
		t.Fatalf("entities = %+v, want the named columns still read correctly", res.Entities)
	}
}
