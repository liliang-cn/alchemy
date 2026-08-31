package rdf

import "testing"

// entityIDFromIRI has to be the exact inverse of entityIRI, including for the
// IDs that need escaping, because a walk built on a best-effort demint would
// hand a caller an identifier that resolves to nothing -- or to something else.
func TestAnEntityIRIRoundTripsBackToItsID(t *testing.T) {
	l := &Loader{opts: Options{RunID: "ld-1"}}
	for _, id := range []string{
		"e1", "person:mira", "a/b", "with space", "unicode-\u00e4\u00f6", "100%", "a#b", "?q=1",
	} {
		iri := l.entityIRI("ld-1", id)
		if got := l.entityIDFromIRI("ld-1", iri); got != id {
			t.Errorf("entityIDFromIRI(entityIRI(%q)) = %q", id, got)
		}
	}
}

// An IRI from another load, or of another kind of record, is not an entity ID
// in this load. Empty is the honest answer; the tail of the string is a guess.
func TestAnIRIThatIsNotThisLoadsEntityIsNotDeminted(t *testing.T) {
	l := &Loader{opts: Options{RunID: "ld-1"}}
	for _, s := range []string{
		l.entityIRI("ld-2", "e1"),
		l.chunkIRI("ld-1", 4),
		l.recordIRI("ld-1", "duplicate", 0),
		"http://example.org/e1",
		"",
	} {
		if got := l.entityIDFromIRI("ld-1", s); got != "" {
			t.Errorf("entityIDFromIRI(%q) = %q, want empty", s, got)
		}
	}
}
