package qdrant

import (
	"context"
	"testing"
)

// The load's default name was a slice of a digest over the whole encoded
// result, because nothing else identified a run. Fingerprint's own comment
// says what that costs: it covers "fields added to alchemy.Result later", so a
// field added to the contract renames every load a buyer has, and every point
// ID with it.
//
// alchemy.Result.Job is the name the producer gave the run — stable across a
// retry, across §8.3's takeover by another node, and across the contract
// growing a field. The fingerprint keeps the job only it can do here: a Qdrant
// point ID must be a UUID or an integer, so the *point* identity still has to
// be derived, and this changes what the load is called rather than how its
// points are addressed.
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

// An explicit ID still wins over both.
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
