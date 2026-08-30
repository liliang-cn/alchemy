package tabular

import (
	"strings"
	"testing"
)

// An id derived from the row's position is an id that changes when the export
// is re-sorted, and a re-import then reads as new data. It is derived from the
// data instead, so reading the same row twice is the same entity.
func TestTheSameRowReadTwiceIsTheSameEntity(t *testing.T) {
	in := "id,city\n1,Paris\n2,Berlin\n"
	first, err := readFixed(t, in, idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Re-read with the rows in the other order: identity must not depend on it.
	second, err := readFixed(t, "id,city\n2,Berlin\n1,Paris\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if first.Entities[0].ID != second.Entities[1].ID || first.Entities[0].ID != "row:1" {
		t.Errorf("ids = %q / %q, want the same id for the same row", first.Entities[0].ID, second.Entities[1].ID)
	}
}

// A row with no identity cannot be re-imported, cannot be referred to by a
// relation, and cannot be corrected later. Synthesising one from the line
// number would hide all three.
func TestRowWithAnEmptyIDIsSkippedAndReported(t *testing.T) {
	res, err := readFixed(t, "id,city\n1,Paris\n,Berlin\n3,Rome\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %+v", res.Entities)
	}
	if len(res.Violations) != 1 || res.Violations[0].Kind != ViolationMissingID {
		t.Fatalf("violations = %+v", res.Violations)
	}
	if !strings.Contains(res.Violations[0].Subject, "line 3") {
		t.Errorf("Subject = %q", res.Violations[0].Subject)
	}
}

// A re-exported file repeating a row loses nothing when the repeat is dropped,
// so the collapse is silent. pkg/source/ddl treats an identical duplicate
// declaration the same way.
func TestIdenticalDuplicateRowsCollapseSilently(t *testing.T) {
	res, err := readFixed(t, "id,city\n1,Paris\n1,Paris\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 1 {
		t.Fatalf("entities = %+v", res.Entities)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none: the two rows agree", res.Violations)
	}
}

// When they disagree, the id column is not identifying rows. The first row wins
// because two entities sharing an id break every consumer that walks the graph
// by id — but which one is right is not in the data, so the loss is reported.
func TestDuplicateIDsThatDifferKeepTheFirstAndAreReported(t *testing.T) {
	res, err := readFixed(t, "id,city\n1,Paris\n1,Berlin\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].Attributes["city"] != "Paris" {
		t.Fatalf("entities = %+v, want the first row kept", res.Entities)
	}
	if len(res.Violations) != 1 || res.Violations[0].Kind != ViolationDuplicateID {
		t.Fatalf("violations = %+v", res.Violations)
	}
	v := res.Violations[0]
	if !strings.Contains(v.Subject, "line 3") || !strings.Contains(v.Detail, "line 2") {
		t.Errorf("violation = %q / %q, want both lines named", v.Subject, v.Detail)
	}
}

// Two rows that produce the same entity are not a disagreement, whatever else
// the file carries beside them. Reporting a column the mapping never reads
// would fill the review queue (§5c) with items a reviewer cannot act on.
func TestDuplicateIDsDifferingOnlyOutsideTheMappingAreNotReported(t *testing.T) {
	res, err := readFixed(t, "id,city,note\n1,Paris,exported 2026-01\n1,Paris,exported 2026-02\n", idCity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 1 {
		t.Fatalf("entities = %+v", res.Entities)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none: both rows make the same entity", res.Violations)
	}
}

// The reader does not edit data. A value it trimmed is a value that no longer
// matches the file, and a consumer checking the graph against the source would
// not find it — a silent edit, which is the class of bug this package is about.
// Identity is the exception: an id that differs from another by a space is a
// duplicate nobody can see, so ids and the cells that point at them are
// normalised, and only they.
func TestValuesAreKeptVerbatimWhileIdentityIsNormalised(t *testing.T) {
	res, err := readFixed(t, "id,city,seller_id\n 1 , Paris ,  7\n", &Mapping{
		EntityType: "Row", IDColumn: "id",
		Attributes: map[string]string{"city": "city"},
		Relations:  []RelationMapping{{Column: "seller_id", RelationType: "SOLD_BY", TargetType: "Seller"}},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Two entities: the row, and the seller its column names. The second is
	// what keeps the edge below from dangling — see referenced in rows.go.
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %+v, want the row and the seller it names", res.Entities)
	}
	if got := res.Entities[0].Attributes["city"]; got != " Paris " {
		t.Errorf("city = %q, want the cell as the file has it", got)
	}
	if res.Entities[0].ID != "row:1" {
		t.Errorf("id = %q, want the padding off the identity", res.Entities[0].ID)
	}
	if len(res.Relations) != 1 || res.Relations[0].To != "seller:7" {
		t.Fatalf("relations = %+v, want the target id normalised the same way", res.Relations)
	}
	// The rule is the same on both sides of the edge: "  7" identifies seller 7,
	// so the entity the edge lands on carries the normalised id too, and the two
	// are the same string or the edge dangles.
	if res.Entities[1].ID != "seller:7" {
		t.Errorf("referenced entity = %+v, want the identity normalised as the edge's is", res.Entities[1])
	}
}
