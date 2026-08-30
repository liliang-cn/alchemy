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

// TestEveryClosedSetHasAWireName holds the other two maps from closed sets to
// their proto enums, for the reason the producers table earned the hard way: a
// Go map returns the zero value for a key it does not have, so a kind added to
// the core module and forgotten here does not fail, it serialises as
// UNSPECIFIED. A conflict that reaches a caller as CONFLICT_KIND_UNSPECIFIED
// is a job held for a reason nobody can read.
//
// Each list is named rather than derived, because Go cannot enumerate a
// string-typed constant set; the proto enum's own length is what catches an
// addition on either side.
func TestEveryClosedSetHasAWireName(t *testing.T) {
	conflicts := []alchemy.ConflictKind{
		alchemy.ConflictEntityAttributes,
		alchemy.ConflictEntityType,
		alchemy.ConflictRelationDirection,
		alchemy.ConflictContradiction,
		alchemy.ConflictRelationAttributes,
		alchemy.ConflictCardinality,
	}
	if got, want := len(alchemyv1.ConflictKind_name), len(conflicts)+1; got != want {
		t.Errorf("the proto declares %d conflict kinds and this test names %d", got-1, len(conflicts))
	}
	for _, k := range conflicts {
		if conflictKinds[k] == alchemyv1.ConflictKind_CONFLICT_KIND_UNSPECIFIED {
			t.Errorf("conflict kind %q has no wire name; it would reach a caller as UNSPECIFIED", k)
		}
	}

	kinds := []alchemy.ProposalKind{
		alchemy.ProposalEntity,
		alchemy.ProposalRelation,
		alchemy.ProposalRelationEnds,
	}
	if got, want := len(alchemyv1.ProposalKind_name), len(kinds)+1; got != want {
		t.Errorf("the proto declares %d proposal kinds and this test names %d", got-1, len(kinds))
	}
	for _, k := range kinds {
		if proposalKinds[k] == alchemyv1.ProposalKind_PROPOSAL_KIND_UNSPECIFIED {
			t.Errorf("proposal kind %q has no wire name; a caller would be handed a change to "+
				"their vocabulary without being told which kind of change it is", k)
		}
	}

	signals := []alchemy.DuplicateSignal{
		alchemy.DuplicateNameAffix,
		alchemy.DuplicateNameAcrossProducers,
		alchemy.DuplicateAlias,
	}
	if got, want := len(alchemyv1.DuplicateSignal_name), len(signals)+1; got != want {
		t.Errorf("the proto declares %d duplicate signals and this test names %d", got-1, len(signals))
	}
	for _, sig := range signals {
		if duplicateSignals[sig] == alchemyv1.DuplicateSignal_DUPLICATE_SIGNAL_UNSPECIFIED {
			t.Errorf("duplicate signal %q has no wire name; a reviewer would be told what asked "+
				"them nothing at all", sig)
		}
	}
}
