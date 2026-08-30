package service

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/wire"
)

// §6's gateway is a translation and never a second source of truth, so
// anything the JSON contract promises has to survive the wire. A violation
// whose structured subject were dropped on the way out would leave every gRPC
// and REST consumer back where it started, parsing prose.
func TestAViolationsStructuredSubjectSurvivesTheWire(t *testing.T) {
	v := alchemy.Violation{
		Kind: alchemy.ViolationRelationNotAllowed, Subject: "a -[OWNS]-> b", Detail: "no",
		About: alchemy.Ref{Kind: alchemy.RefRelation, From: "a", To: "b", Type: "OWNS", Key: "fk_left"},
	}
	got := violationToProto(v).GetAbout()
	if got.GetFrom() != "a" || got.GetTo() != "b" || got.GetType() != "OWNS" || got.GetKey() != "fk_left" {
		t.Fatalf("about = %+v, want the edge in fields", got)
	}
	if got.GetKind() != wire.RefKindToProto[alchemy.RefRelation] {
		t.Fatalf("kind = %v, want a relation", got.GetKind())
	}
}

// A violation about a file rather than a record carries no ref, and the wire
// says so by absence rather than by an empty message: "this is about nothing
// in the graph" and "this is about the entity with no id" are different claims.
func TestAViolationAboutNoRecordCarriesNoRef(t *testing.T) {
	v := alchemy.Violation{Kind: alchemy.ViolationMalformedRow, Subject: "row 4"}
	if got := violationToProto(v).GetAbout(); got != nil {
		t.Fatalf("about = %+v, want nothing", got)
	}
}

// The ref on a violation carries no provenance, because the violation already
// does. Two copies of one fact on one message is two answers that can disagree.
func TestAViolationsRefCarriesNoProvenanceOfItsOwn(t *testing.T) {
	v := alchemy.Violation{
		Kind: alchemy.ViolationUnknownEntityType, Subject: "n1",
		About:      alchemy.Ref{Kind: alchemy.RefEntity, ID: "n1", Type: "Flag"},
		Provenance: alchemy.Provenance{Source: "a.md", Chunk: 3},
	}
	out := violationToProto(v)
	if out.GetAbout().GetProvenance() != nil {
		t.Error("the ref carries a second provenance beside the violation's own")
	}
	if out.GetProvenance().GetSource() != "a.md" {
		t.Errorf("the violation lost its own provenance: %+v", out.GetProvenance())
	}
}

// §5's numbers ride on the first page of a paged result, and a reader that got
// entity and relation totals but no chunk or vector totals could not say when
// it had seen them all.
func TestTheCountsCarryChunksAndVectors(t *testing.T) {
	got := countsToProto(alchemy.Counts{Chunks: 30, Vectors: 29})
	if got.GetChunks() != 30 || got.GetVectors() != 29 {
		t.Fatalf("counts = %+v, want 30 chunks and 29 vectors", got)
	}
}

// A result's identity is the one thing a store cannot derive without digesting
// the whole graph, and a wire that dropped it would send every consumer back to
// doing exactly that.
func TestTheResultCarriesItsJobOverTheWire(t *testing.T) {
	if got := resultToProto(alchemy.Result{Job: "job-42"}).GetJob(); got != "job-42" {
		t.Fatalf("job = %q, want the job that produced it", got)
	}
}
