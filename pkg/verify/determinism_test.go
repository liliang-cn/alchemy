package verify_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// messy is a graph carrying every fault this package knows about at once, with
// enough entities and attributes that a map-ordered implementation would be
// caught by the repeat runs below rather than by luck.
func messy() verify.Input {
	in := verify.Input{Vocabulary: vocab(), OntologyID: "sds@3"}
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("c%d", i)
		in.Entities = append(in.Entities,
			alchemy.Entity{ID: id, Type: "cluster", Name: "prod", Attributes: map[string]any{"region": "eu-west", "version": "3.1", "zone": "a"}, Provenance: fromSchema},
			alchemy.Entity{ID: id, Type: "StoragePool", Name: "production", Attributes: map[string]any{"region": "us-east", "version": "3.1", "zone": "b"}, Provenance: fromPDF},
			alchemy.Entity{ID: "n" + id, Type: "Node", Provenance: fromSchema},
			alchemy.Entity{ID: "x" + id, Type: "Wormhole", Provenance: fromPDF},
			// Two affix pairs per iteration, so the duplicate scan is walking
			// a map big enough for its order to show. A pair per node id keeps
			// them distinct rather than collapsing into one crowded key.
			alchemy.Entity{ID: "p" + id, Type: "Cluster", Name: "pool " + id, Provenance: fromPDF},
			alchemy.Entity{ID: "q" + id, Type: "Cluster", Name: "pool " + id + " cluster", Provenance: fromOtherPDF},
		)
		in.Relations = append(in.Relations,
			alchemy.Relation{From: id, To: "n" + id, Type: "CONTAINS", Attributes: map[string]any{"on_delete": "cascade", "card": "1:n"}, Provenance: fromSchema},
			alchemy.Relation{From: id, To: "n" + id, Type: "CONTAINS", Attributes: map[string]any{"on_delete": "restrict", "card": "1:1"}, Provenance: fromPDF},
			alchemy.Relation{From: "n" + id, To: id, Type: "MENTIONS", Provenance: fromPDF},
			alchemy.Relation{From: id, To: "n" + id, Type: "MENTIONS", Provenance: fromOtherPDF},
			alchemy.Relation{From: id, To: "ghost", Type: "CONTAINS", Provenance: fromPDF},
			alchemy.Relation{From: id, To: "n" + id, Type: "TELEPORTS_TO", Provenance: fromPDF},
		)
	}
	return in
}

// §7.1: a graph re-extracted is compared against the last one. A report whose
// order moves between two runs of the same input cannot be diffed, and every
// map in this package is a chance to lose that.
func TestCheckIsByteForByteRepeatable(t *testing.T) {
	in := messy()
	first, err := json.Marshal(verify.Check(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(first) < 1000 {
		t.Fatalf("fixture produced almost nothing (%d bytes); it is not testing order", len(first))
	}
	for i := 0; i < 50; i++ {
		again, err := json.Marshal(verify.Check(in))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differs from run 0", i)
		}
	}
}

// The counts describe the slices they are returned with, in every graph rather
// than in the one a test happened to write down.
func TestCountsDescribeTheReportTheyAreReturnedWith(t *testing.T) {
	got := verify.Check(messy())
	c := got.Counts
	if c.Entities != len(got.Entities) || c.Relations != len(got.Relations) {
		t.Fatalf("counts %+v do not describe %d entities and %d relations", c, len(got.Entities), len(got.Relations))
	}
	if c.Violations != len(got.Violations) || c.Conflicts != len(got.Conflicts) {
		t.Fatalf("counts %+v do not describe %d violations and %d conflicts", c, len(got.Violations), len(got.Conflicts))
	}
	if c.Duplicates != len(got.Duplicates) {
		t.Fatalf("counts %+v do not describe %d duplicates", c, len(got.Duplicates))
	}
	if c.Deterministic+c.Inferred != c.Relations {
		t.Fatalf("deterministic %d + inferred %d != relations %d", c.Deterministic, c.Inferred, c.Relations)
	}
	if c.Conflicts == 0 || c.Violations == 0 || c.Duplicates == 0 {
		t.Fatalf("fixture is not exercising all three jobs: %+v", c)
	}
}
