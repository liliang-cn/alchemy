package qdrant

import (
	"regexp"
	"testing"
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
