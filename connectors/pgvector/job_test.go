package pgvector

import (
	"context"
	"testing"
)

// The load's default name was a slice of a digest over the whole encoded
// result, because nothing else identified a run. That is the identity
// Fingerprint's own comment warns about one paragraph later: it covers "every
// field, including ones added to alchemy.Result later", so a field added to the
// contract renames every load a buyer has.
//
// alchemy.Result.Job is the name the producer gave the run. It is stable across
// a retry, stable across §8.3's takeover by another node, and unchanged by a
// field being added to the result. The fingerprint keeps its real job — the
// dedupe check that makes an identical re-load a no-op — and stops being asked
// to be a name as well.
func TestALoadIsNamedAfterTheJobThatProducedTheResult(t *testing.T) {
	l := newFixture(t).open(t, Config{})
	res := smallResult(4)
	res.Job = "job-42"

	out, err := l.Load(context.Background(), res, LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ID != "job-42" {
		t.Fatalf("ID = %q, want the job that produced the result", out.ID)
	}
}

// A result nobody named still gets a name, and it is still the fingerprint's:
// a library caller running the pipeline directly has no job store, and a load
// with no name at all could not be found again.
func TestAResultWithNoJobFallsBackToTheFingerprint(t *testing.T) {
	l := newFixture(t).open(t, Config{})
	out, err := l.Load(context.Background(), smallResult(4), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ID != "ld_"+out.Fingerprint[:24] {
		t.Fatalf("ID = %q, want the fingerprint-derived name", out.ID)
	}
}

// An explicit ID still wins over both. A caller loading one job's graph under
// two names is doing something neither the result nor the digest knows about.
func TestAnExplicitLoadIDStillWins(t *testing.T) {
	l := newFixture(t).open(t, Config{})
	res := smallResult(4)
	res.Job = "job-42"
	out, err := l.Load(context.Background(), res, LoadOptions{ID: "rehearsal"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ID != "rehearsal" {
		t.Fatalf("ID = %q, want the caller's", out.ID)
	}
}
