package wire_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/wire"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// These tests moved here from pkg/service, following the tables they hold to
// account. They existed before this package did, and they are the reason it
// was safe to move anything: a table that goes short does not raise an error,
// it returns a zero value, and the only thing that catches that is a test that
// names every member of the closed set out loud.

// roundTrip is the wire, both ways: what a caller on the far side of gRPC gets
// back after a Provenance has been serialised and deserialised.
func roundTrip(p alchemy.Provenance) alchemy.Provenance {
	return wire.ProvenanceFromProto(wire.ProvenanceToProto(p))
}

// TestEveryProvenanceFieldSurvivesTheWire round-trips a Provenance with every
// field set to a distinct value.
//
// It exists because three of them did not. alchemy.Provenance grew By and At;
// ProvenanceToProto copied ten fields and kept copying ten, so a record a
// person had signed went out over gRPC with the signature missing. Worse, the
// producer table — a map from a closed set — gained no entry for
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

// TestEveryProducerHasAWireName holds the producer table to alchemy's own
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

// TestEveryClosedSetHasAWireName holds the rest of the maps from closed sets to
// their proto enums, for the reason the producer table earned the hard way: a
// Go map returns the zero value for a key it does not have, so a kind added to
// the core module and forgotten here does not fail, it serialises as
// UNSPECIFIED. A conflict that reaches a caller as CONFLICT_KIND_UNSPECIFIED
// is a job held for a reason nobody can read.
//
// Each list is named rather than derived, because Go cannot enumerate a
// string-typed constant set; the proto enum's own length is what catches an
// addition on either side.
//
// It covers every table in this package because every table is now in this
// package. While they were unexported in pkg/service, three of them were
// checked and the other eight were not, which is not a judgement anybody made
// — it is where the file happened to stop.
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
		if wire.ConflictKindToProto[k] == alchemyv1.ConflictKind_CONFLICT_KIND_UNSPECIFIED {
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
		if wire.ProposalKindToProto[k] == alchemyv1.ProposalKind_PROPOSAL_KIND_UNSPECIFIED {
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
		if wire.DuplicateSignalToProto[sig] == alchemyv1.DuplicateSignal_DUPLICATE_SIGNAL_UNSPECIFIED {
			t.Errorf("duplicate signal %q has no wire name; a reviewer would be told what asked "+
				"them nothing at all", sig)
		}
	}

	violations := []alchemy.ViolationKind{
		alchemy.ViolationUnknownEntityType,
		alchemy.ViolationUnknownRelationType,
		alchemy.ViolationRelationNotAllowed,
		alchemy.ViolationDanglingRelation,
		alchemy.ViolationMalformedRow,
		alchemy.ViolationUnnamedColumn,
		alchemy.ViolationMissingID,
		alchemy.ViolationDuplicateID,
	}
	if got, want := len(alchemyv1.ViolationKind_name), len(violations)+1; got != want {
		t.Errorf("the proto declares %d violation kinds and this test names %d", got-1, len(violations))
	}
	for _, k := range violations {
		if wire.ViolationKindToProto[k] == alchemyv1.ViolationKind_VIOLATION_KIND_UNSPECIFIED {
			t.Errorf("violation kind %q has no wire name; §4 promises this field is a closed "+
				"set, and a consumer switching on it would fall through", k)
		}
	}

	refs := []alchemy.RefKind{alchemy.RefEntity, alchemy.RefRelation}
	if got, want := len(alchemyv1.RefKind_name), len(refs)+1; got != want {
		t.Errorf("the proto declares %d ref kinds and this test names %d", got-1, len(refs))
	}
	for _, k := range refs {
		if wire.RefKindToProto[k] == alchemyv1.RefKind_REF_KIND_UNSPECIFIED {
			t.Errorf("ref kind %q has no wire name; a finding would name a record without "+
				"saying which half of the graph it is in", k)
		}
	}

	states := []alchemy.JobState{
		alchemy.JobPending, alchemy.JobRunning, alchemy.JobNeedsReview,
		alchemy.JobSucceeded, alchemy.JobFailed, alchemy.JobExpired, alchemy.JobCancelled,
	}
	if got, want := len(alchemyv1.JobState_name), len(states)+1; got != want {
		t.Errorf("the proto declares %d job states and this test names %d", got-1, len(states))
	}
	for _, s := range states {
		if wire.JobStateToProto[s] == alchemyv1.JobState_JOB_STATE_UNSPECIFIED {
			t.Errorf("job state %q has no wire name; a caller polling a job would be told "+
				"nothing about where it is", s)
		}
	}

	sources := []alchemy.SourceKind{
		alchemy.SourceTabular, alchemy.SourceDDL, alchemy.SourceDocument, alchemy.SourceGraph,
	}
	if got, want := len(alchemyv1.SourceKind_name), len(sources)+1; got != want {
		t.Errorf("the proto declares %d source kinds and this test names %d", got-1, len(sources))
	}
	for _, k := range sources {
		if wire.SourceKindToProto[k] == alchemyv1.SourceKind_SOURCE_KIND_UNSPECIFIED {
			t.Errorf("source kind %q has no wire name; an upload would be routed to no reader "+
				"at all", k)
		}
	}
}

// TestEveryReviewSetHasAWireName is the same check for pkg/review's three
// closed sets. They are held apart from alchemy's only because they belong to
// a different package, not because they are less able to go short: §5c calls
// the verbs "exactly four, and the set is closed", and a fifth arriving as
// UNSPECIFIED would be a reviewer's answer the service cannot act on.
func TestEveryReviewSetHasAWireName(t *testing.T) {
	kinds := []review.Kind{
		review.KindConflict, review.KindViolation, review.KindGuess,
		review.KindDuplicate, review.KindLowConfidence,
	}
	if got, want := len(alchemyv1.ReviewKind_name), len(kinds)+1; got != want {
		t.Errorf("the proto declares %d review kinds and this test names %d", got-1, len(kinds))
	}
	for _, k := range kinds {
		if wire.ReviewKindToProto[k] == alchemyv1.ReviewKind_REVIEW_KIND_UNSPECIFIED {
			t.Errorf("review kind %q has no wire name; an item would arrive in a queue without "+
				"saying why it is there", k)
		}
	}

	verbs := []review.Verb{review.VerbAccept, review.VerbReject, review.VerbEdit, review.VerbAlways}
	if got, want := len(alchemyv1.ReviewVerb_name), len(verbs)+1; got != want {
		t.Errorf("the proto declares %d review verbs and this test names %d", got-1, len(verbs))
	}
	for _, v := range verbs {
		if wire.VerbToProto[v] == alchemyv1.ReviewVerb_REVIEW_VERB_UNSPECIFIED {
			t.Errorf("review verb %q has no wire name; a reviewer's answer would reach the "+
				"service as no answer", v)
		}
	}

	// Origin is checked through RuleOriginToProto rather than through the map,
	// because the map is not what callers use: review's zero Origin means
	// OriginReviewed, and only the function says so. A table lookup on the zero
	// value would send UNSPECIFIED and make the weaker warrant — a policy
	// somebody wrote in advance — indistinguishable from the stronger one.
	origins := []review.Origin{review.OriginReviewed, review.OriginAuthored}
	if got, want := len(alchemyv1.RuleOrigin_name), len(origins)+1; got != want {
		t.Errorf("the proto declares %d rule origins and this test names %d", got-1, len(origins))
	}
	for _, o := range origins {
		if wire.RuleOriginToProto(o) == alchemyv1.RuleOrigin_RULE_ORIGIN_UNSPECIFIED {
			t.Errorf("rule origin %q has no wire name", o)
		}
		if got := wire.OriginFromProto[wire.RuleOriginToProto(o)]; got != o {
			t.Errorf("rule origin %q came back as %q", o, got)
		}
	}
	// The zero value is a reviewer's rule and has to say so on the wire.
	if got := wire.RuleOriginToProto(review.Origin("")); got != alchemyv1.RuleOrigin_RULE_ORIGIN_REVIEWED {
		t.Errorf("an unset Origin went out as %v; silence means a reviewer's rule, and a "+
			"reader who cannot tell the two warrants apart will read the weaker as the "+
			"stronger", got)
	}
}
