package alchemy

import "testing"

// Two records that assert the same edge are one edge, and every store has to
// agree about that or the same graph has a different edge count in each of
// them. The rule is the one Relation.Key already states — identity is the
// ends, the type and the producer's key — and this is that rule as something
// a caller can call rather than reimplement.
func TestTwoRecordsOfOneEdgeShareAnIdentity(t *testing.T) {
	a := Relation{From: "cluster:superai", To: "db:cortexdb", Type: "USES",
		Provenance: Provenance{Source: "a.pdf", Chunk: 1, Confidence: 0.9}}
	b := Relation{From: "cluster:superai", To: "db:cortexdb", Type: "USES",
		Attributes: map[string]any{"since": "2024"},
		Provenance: Provenance{Source: "b.pdf", Chunk: 40, Confidence: 0.4}}
	if a.Identity() != b.Identity() {
		t.Fatalf("Identity() = %q and %q; two records asserting one edge must be one edge", a.Identity(), b.Identity())
	}
}

// The case Relation.Key exists for: a table that references another twice
// states two foreign keys, both correct, differing only in which of them they
// are. A store keyed on the ends and the type alone writes one edge and loses
// the other.
func TestTwoParallelEdgesHaveTwoIdentities(t *testing.T) {
	a := Relation{From: "table:nodes", To: "table:nodes", Type: "REFERENCES", Key: "fk_source"}
	b := Relation{From: "table:nodes", To: "table:nodes", Type: "REFERENCES", Key: "fk_target"}
	if a.Identity() == b.Identity() {
		t.Fatalf("Identity() = %q for both; two foreign keys are two edges", a.Identity())
	}
}

// Direction is part of what an edge is. verify's conflict key is undirected on
// purpose — a reversal is the question it exists to find — and a store keyed
// the same way would write A→B and B→A as one row.
func TestAnEdgeAndItsReverseAreTwoIdentities(t *testing.T) {
	a := Relation{From: "a", To: "b", Type: "USES"}
	b := Relation{From: "b", To: "a", Type: "USES"}
	if a.Identity() == b.Identity() {
		t.Fatal("Identity() is undirected; a store keyed on it would merge an edge with its reversal")
	}
}

// The framing is length-prefixed, not delimited, for the reason pkg/cache
// gives: entity IDs are folded document text and contain every byte sooner or
// later, so a separator can be forged out of the values themselves.
func TestNoPairOfEndsCanForgeAnotherEdgesIdentity(t *testing.T) {
	a := Relation{From: "a", To: "b\x00USES\x00c", Type: "X"}
	b := Relation{From: "a\x00b", To: "c", Type: "USES\x00X"}
	if a.Identity() == b.Identity() {
		t.Fatalf("two different edges share identity %q; the framing is forgeable", a.Identity())
	}
}

// A Go consumer and a Python one must land on the same string or the same
// corpus is two corpora in two stores. Pinning the digest is how that stops
// being a claim: this value is what the documented algorithm produces, and a
// change to the algorithm has to change this line and say why.
//
// "What the documented algorithm produces" was itself only a claim until it
// was checked. The constant has since been reproduced by an independent
// implementation of the paragraph on Identity — in Python, reading the comment
// and not this file — and matched byte for byte. That is the property being
// asserted: not that Go computes this twice the same way, which a recording of
// its own output would also show, but that the prose and the code describe one
// algorithm. pkg/sink and pkg/review pin their digests the same way and for
// the same reason.
func TestTheIdentityOfAnEdgeIsAFixedValue(t *testing.T) {
	r := Relation{From: "cluster:superai", To: "db:cortexdb", Type: "USES"}
	const want = "ee7d7e492657407f496c569f3b31adacdabaaebbb39d5baa1fbf7654124de727"
	if got := r.Identity(); got != want {
		t.Fatalf("Identity() = %q, want %q", got, want)
	}
}
