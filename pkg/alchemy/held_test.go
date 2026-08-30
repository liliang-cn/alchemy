package alchemy

import "testing"

// §7.3 is the one rule this design refuses to let a caller opt out of, and a
// rule that lives in a package a consumer need not import is a rule consumers
// forget. Four sinks written independently each had to reach for it; a fifth
// that does not is a graph that contradicts itself, written to a store,
// silently.
func TestAResultWithAnUnansweredConflictIsHeld(t *testing.T) {
	res := Result{Conflicts: []Conflict{{
		Kind: ConflictEntityType, Subject: "n1",
		Left:  Claim{Provenance: Provenance{Source: "schema.sql"}},
		Right: Claim{Provenance: Provenance{Source: "contract.pdf"}},
	}}}
	if open := res.Held(); len(open) != 1 {
		t.Fatalf("Held() = %+v, want the unanswered conflict", open)
	}
}

// A conflict a person answered stays in the result carrying their name (§5b),
// and a job that stayed held on an answered question would be a queue nobody
// could empty. Either side carrying a reviewer is enough: the losing claim's
// record may have been deleted along with it.
func TestAConflictEitherSideOfWhichWasSignedIsNotHeld(t *testing.T) {
	for _, tc := range []struct{ left, right string }{
		{"ana", ""},
		{"", "ana"},
		{"ana", "bo"},
	} {
		res := Result{Conflicts: []Conflict{{
			Left:  Claim{Provenance: Provenance{ReviewedBy: tc.left}},
			Right: Claim{Provenance: Provenance{ReviewedBy: tc.right}},
		}}}
		if open := res.Held(); len(open) != 0 {
			t.Fatalf("Held() = %+v for (%q, %q), want nothing: a person answered it", open, tc.left, tc.right)
		}
	}
}

// Violations are deliberately on the other side of §7.3's line: one source
// saying something the ontology forbids is attributable and excludable, and
// the rest of the graph is usable without it.
func TestAViolationDoesNotHoldAResult(t *testing.T) {
	res := Result{Violations: []Violation{{Kind: ViolationUnknownEntityType, Subject: "n1"}}}
	if open := res.Held(); len(open) != 0 {
		t.Fatalf("Held() = %+v, want nothing: a violation never holds a job", open)
	}
}
