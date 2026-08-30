package service

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// roundTrip is the wire, both ways: what a caller on the far side of gRPC gets
// back after this package has serialised and deserialised a Provenance.
func roundTrip(p alchemy.Provenance) alchemy.Provenance {
	return provenanceFromProto(provenanceToProto(p))
}

// TestEveryProvenanceFieldSurvivesTheWire round-trips a Provenance with every
// field set to a distinct value.
//
// It exists because three of them did not. alchemy.Provenance grew By and At;
// provenanceToProto copied ten fields and kept copying ten, so a record a
// person had signed went out over gRPC with the signature missing. Worse, the
// producers table — a map from a closed set — gained no entry for
// alchemy.ProducerHuman, and a Go map returns the zero value for a key it does
// not have: every human record was serialised as PRODUCER_UNSPECIFIED. Not an
// error, not a log line, just a fact about who said something quietly becoming
// a fact about nobody.
//
// A struct copied field by field into another struct cannot be checked by the
// compiler, so it has to be checked here. Distinct values in every field are
// what makes a transposition visible as well as an omission.
func TestEveryProvenanceFieldSurvivesTheWire(t *testing.T) {
	want := alchemy.Provenance{
		Source: "profile.pdf", Chunk: 4, Producer: alchemy.ProducerHuman,
		Model: "gemini-3.7-flash-high", Ontology: "freight-ops@7", Chunking: "heading",
		Confidence: 0.82, ReviewedBy: "liliang", RuleSet: "7ceaca1ff5eb9616",
		RuledBy: "collapse-imports", By: "liliang", At: "2026-08-30T09:00:00Z",
	}
	if got := roundTrip(want); got != want {
		t.Fatalf("provenance did not survive the wire\n got %+v\nwant %+v", got, want)
	}
}

// TestEveryProducerHasAWireName holds the producers table to alchemy's own
// list, so a producer added to the core module cannot reach the wire as
// PRODUCER_UNSPECIFIED.
//
// The list here is the one place this test cannot derive: Go has no way to
// enumerate the values of a string-typed constant set. So it names them, and
// the proto enum's own length is what catches an addition on either side — if
// alchemy grows a producer and this list does not, the enum count moves and
// this fails.
func TestEveryProducerHasAWireName(t *testing.T) {
	all := []alchemy.Producer{
		alchemy.ProducerDDL,
		alchemy.ProducerGraphImport,
		alchemy.ProducerTabular,
		alchemy.ProducerLLMExtract,
		alchemy.ProducerHuman,
	}
	// The enum has one extra member, PRODUCER_UNSPECIFIED, which is the zero
	// value and deliberately has no alchemy.Producer behind it.
	if got, want := len(alchemyv1.Producer_name), len(all)+1; got != want {
		t.Fatalf("the proto declares %d producers and this test names %d; a producer added to "+
			"one side and not the other serialises as PRODUCER_UNSPECIFIED, which is not an "+
			"error and not a log line — it is a record that stops saying who made it",
			got-1, len(all))
	}
	for _, p := range all {
		got := roundTrip(alchemy.Provenance{Producer: p}).Producer
		if got != p {
			t.Errorf("producer %q came back as %q; it has no entry in the wire table", p, got)
		}
	}
}
