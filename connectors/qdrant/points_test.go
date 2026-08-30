package qdrant

import (
	"regexp"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Qdrant will only accept a point ID that is an unsigned integer or a UUID, so
// whatever scheme this connector chooses has to end in one of those two
// shapes. A counter is not available — two processes loading the same result
// must produce the same IDs, or a retry doubles the store — so it is a UUID,
// and it has to look like one to the server.
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestPointIDIsAUUIDAndIsDeterministic(t *testing.T) {
	a := pointID("fp1", kindEntity, "SuperAI")
	b := pointID("fp1", kindEntity, "SuperAI")
	if a != b {
		t.Fatalf("pointID is not deterministic: %s vs %s", a, b)
	}
	if !uuidRE.MatchString(a) {
		t.Fatalf("pointID = %q, which Qdrant will refuse: it takes a UUID or an unsigned integer", a)
	}
}

// The same record in two different results must not land on one point. The
// load's fingerprint is in the ID for that reason: Entity.ID is stable within
// one result and says nothing across runs, so two runs that both call
// something "e1" are two things and an upsert that merged them would silently
// overwrite one graph with another.
func TestPointIDSeparatesLoadsRecordsAndKinds(t *testing.T) {
	base := pointID("fp1", kindEntity, "e1")
	for name, other := range map[string]string{
		"another load":  pointID("fp2", kindEntity, "e1"),
		"another key":   pointID("fp1", kindEntity, "e2"),
		"another kind":  pointID("fp1", kindRelation, "e1"),
		"the load mark": pointID("fp1", kindLoad, "e1"),
	} {
		if other == base {
			t.Errorf("%s collides with the base point: both are %s", name, other)
		}
	}
}

// TestEveryKindThisStoreWritesCanBeReadBack walks the kinds and asserts each
// one lands somewhere in Records.
//
// A Go switch over a closed set is not checked for exhaustiveness, and the
// missing case here fails in the worst direction there is: the point is
// written, the filter matches it, the scroll returns it, and Records.add drops
// it with no error anywhere. The buyer's store holds a record and the buyer's
// reader cannot see it, and nothing at any layer says so. That is a map from a
// closed set returning its zero value, spelled as a switch.
//
// kindLoad is excluded because it is the marker rather than a record: Findings
// reads it, and Records deliberately does not put a load marker in a list of
// what the corpus contains.
func TestEveryKindThisStoreWritesCanBeReadBack(t *testing.T) {
	for _, k := range []kind{kindEntity, kindRelation, kindChunk, kindViolation, kindDuplicate, kindSupersession} {
		t.Run(string(k), func(t *testing.T) {
			var got Records
			got.add(map[string]any{keyKind: string(k), keyLoad: "ld-1"})
			n := len(got.Entities) + len(got.Relations) + len(got.Chunks) +
				len(got.Violations) + len(got.Duplicates) + len(got.Supersessions)
			if n != 1 {
				t.Fatalf("a %s point read back as %d records, want 1: this store writes it and no reader "+
					"would ever see it", k, n)
			}
		})
	}
}

// And a retirement survives the round trip field for field, because the shape
// it is stored in is nested and a nested read that returns the zero value is
// the same silence one field down.
func TestARetirementSurvivesTheRoundTrip(t *testing.T) {
	want := alchemy.Supersession{
		Retires: "e-cto-ada",
		By:      alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e2", Type: "HAS_CTO", Key: "fk_cto"},
		Reason:  "the office changed hands in March",
		Provenance: alchemy.Provenance{
			Source: "correction.md", Chunk: -1, Producer: alchemy.ProducerHuman,
		},
	}
	b := supersessionPoints("ld-1", "fp", 0, []alchemy.Supersession{want})
	if len(b.points) != 1 {
		t.Fatalf("%d points for one retirement", len(b.points))
	}
	got := readSupersession(b.points[0].Payload)
	if got.Retires != want.Retires || got.Reason != want.Reason {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.By != want.By {
		t.Errorf("the Ref that replaces it = %+v, want %+v: a reader here cannot follow an id, so what is "+
			"not on the point is not answerable at all", got.By, want.By)
	}
	if got.Provenance.Producer != want.Provenance.Producer || got.Provenance.Source != want.Provenance.Source {
		t.Errorf("provenance = %+v, want the claim to say whose word it is on", got.Provenance)
	}
	// Two retirements of one record by two people are two claims, so they must
	// not collapse onto one point.
	two := supersessionPoints("ld-1", "fp", 0, []alchemy.Supersession{want, want})
	if two.points[0].ID == two.points[1].ID {
		t.Error("two retirements of one record derive one point, so the second person disappears")
	}
}
