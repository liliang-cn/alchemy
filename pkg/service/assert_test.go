package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// assertOntology declares one relation between two entity types, so that a
// test can assert an edge the vocabulary knows and an edge it does not without
// the two differing in anything else.
const assertOntology = `{
  "id": "crm@1",
  "parts": {
    "prose": {
      "entities": [{"name": "Customer"}, {"name": "Region"}],
      "relations": [{"name": "OPERATES_IN", "from": ["Customer"], "to": ["Region"]}]
    }
  }
}`

func acme() *alchemyv1.Entity {
	return &alchemyv1.Entity{Id: "customer:acme", Type: "Customer", Name: "Acme"}
}

func emea() *alchemyv1.Entity {
	return &alchemyv1.Entity{Id: "region:emea", Type: "Region", Name: "EMEA"}
}

// asserted calls Assert and fails the test if the call did not succeed, so the
// tests below read as statements about the graph rather than about error
// handling.
func asserted(t *testing.T, cli alchemyv1.AlchemyClient, req *alchemyv1.AssertRequest) *alchemyv1.Result {
	t.Helper()
	res, err := cli.Assert(authed(context.Background()), req)
	if err != nil {
		t.Fatalf("Assert: %v", err)
	}
	return res
}

// The whole of what the producer buys: a reader of this edge can name the
// person, and the date they said it. §5b sells "who says so", and until this
// producer existed the honest answer for a fact nobody had written down was
// "a JSON file somebody imported".
func TestAnAssertionNamesThePersonAndTheMomentTheySaidIt(t *testing.T) {
	before := time.Now().Add(-time.Second)
	cli := dial(t, harness{})

	res := asserted(t, cli, &alchemyv1.AssertRequest{
		Entities: []*alchemyv1.Entity{acme()},
		By:       "dana@example.com",
	})

	if n := len(res.GetEntities()); n != 1 {
		t.Fatalf("entities = %d, want 1", n)
	}
	p := res.GetEntities()[0].GetProvenance()
	if p.GetProducer() != alchemyv1.Producer_PRODUCER_HUMAN {
		t.Errorf("producer = %v, want PRODUCER_HUMAN; a person's statement recorded as anything else is the record this endpoint exists to stop making", p.GetProducer())
	}
	if p.GetBy() != "dana@example.com" {
		t.Errorf("by = %q, want the asserter", p.GetBy())
	}
	at, err := time.Parse(time.RFC3339, p.GetAt())
	if err != nil {
		t.Fatalf("at = %q, which is not RFC 3339: %v", p.GetAt(), err)
	}
	if at.Before(before) || at.After(time.Now().Add(time.Second)) {
		t.Errorf("at = %v, which is not a moment during this call", at)
	}
	if p.GetChunk() != -1 {
		t.Errorf("chunk = %d, want -1; an assertion has no chunks, and any other number is a citation into nothing", p.GetChunk())
	}
	if p.GetSource() != "dana@example.com" {
		t.Errorf("source = %q; the source of an assertion is the asserter, because there is no document", p.GetSource())
	}
}

// The field is stamped, never read. A caller who could fill it in could assert
// on somebody else's behalf and could backdate it, and either one turns "a
// named person said this" back into "somebody typed this".
func TestAnAssertionIsStampedWithTheAsserterNotTheCaller(t *testing.T) {
	cli := dial(t, harness{})

	forged := &alchemyv1.Provenance{
		Producer: alchemyv1.Producer_PRODUCER_LLM_EXTRACT,
		By:       "someone-else@example.com",
		At:       "1999-01-01T00:00:00Z",
		Source:   "invented.md",
		Chunk:    7,
	}
	ent := acme()
	ent.Provenance = forged
	rel := &alchemyv1.Relation{
		From: "customer:acme", To: "region:emea", Type: "OPERATES_IN", Provenance: forged,
	}

	res := asserted(t, cli, &alchemyv1.AssertRequest{
		Entities:  []*alchemyv1.Entity{ent, emea()},
		Relations: []*alchemyv1.Relation{rel},
		By:        "dana@example.com",
	})

	for _, p := range []*alchemyv1.Provenance{res.GetEntities()[0].GetProvenance(), res.GetRelations()[0].GetProvenance()} {
		if p.GetBy() != "dana@example.com" {
			t.Errorf("by = %q, want the asserter; a caller must not be able to assert on somebody else's behalf", p.GetBy())
		}
		if p.GetAt() == "1999-01-01T00:00:00Z" {
			t.Errorf("at = %q, the caller's; an assertion cannot be backdated", p.GetAt())
		}
		if p.GetProducer() != alchemyv1.Producer_PRODUCER_HUMAN {
			t.Errorf("producer = %v, want PRODUCER_HUMAN", p.GetProducer())
		}
		if p.GetSource() != "dana@example.com" || p.GetChunk() != -1 {
			t.Errorf("source/chunk = %q/%d, want the asserter and -1", p.GetSource(), p.GetChunk())
		}
	}
}

// The obligation alchemy.ProducerHuman carries, refused at the door rather
// than reported afterwards: an assertion nobody is named for is an anonymous
// claim wearing a person's badge, and there is no state of the world in which
// it is a valid request.
func TestAnAssertionNobodyIsNamedForIsRefused(t *testing.T) {
	cli := dial(t, harness{})

	_, err := cli.Assert(authed(context.Background()), &alchemyv1.AssertRequest{
		Entities: []*alchemyv1.Entity{acme()},
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

// An assertion that states nothing is not an empty graph, it is a mistake.
func TestAnAssertionWithNothingInItIsRefused(t *testing.T) {
	cli := dial(t, harness{})

	_, err := cli.Assert(authed(context.Background()), &alchemyv1.AssertRequest{By: "dana@example.com"})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

// §5: the graph is never more permissive than the vocabulary. A person
// asserting a type nobody declared is exactly the case that has to be visible
// instead of quietly accepted — and visible is a violation on the result, not
// a refusal, because the person may well be right and the ontology behind.
func TestAnAssertionOfAnUndeclaredRelationTypeComesBackWithAViolation(t *testing.T) {
	cli := dial(t, harness{})

	res := asserted(t, cli, &alchemyv1.AssertRequest{
		Entities:  []*alchemyv1.Entity{acme(), emea()},
		Relations: []*alchemyv1.Relation{{From: "customer:acme", To: "region:emea", Type: "SUPPLIES"}},
		By:        "dana@example.com",
		Ontology:  assertOntology,
	})

	var named bool
	for _, v := range res.GetViolations() {
		if v.GetKind() == alchemyv1.ViolationKind_VIOLATION_KIND_UNKNOWN_RELATION_TYPE && strings.Contains(v.GetDetail(), "SUPPLIES") {
			named = true
		}
	}
	if !named {
		t.Fatalf("no violation names SUPPLIES; violations = %+v", res.GetViolations())
	}
	if res.GetCounts().GetViolations() != int32(len(res.GetViolations())) {
		t.Errorf("counts.violations = %d, violations = %d", res.GetCounts().GetViolations(), len(res.GetViolations()))
	}
	if p := res.GetRelations()[0].GetProvenance(); p.GetOntology() != "crm@1" {
		t.Errorf("ontology = %q, want crm@1; a record checked against a vocabulary has to say which one", p.GetOntology())
	}
}

// The other half of the same rule: an assertion the ontology does declare is
// not made suspicious by having been checked.
func TestAnAssertionTheOntologyDeclaresReportsNoViolations(t *testing.T) {
	cli := dial(t, harness{})

	res := asserted(t, cli, &alchemyv1.AssertRequest{
		Entities:  []*alchemyv1.Entity{acme(), emea()},
		Relations: []*alchemyv1.Relation{{From: "customer:acme", To: "region:emea", Type: "OPERATES_IN"}},
		By:        "dana@example.com",
		Ontology:  assertOntology,
	})

	if n := len(res.GetViolations()); n != 0 {
		t.Fatalf("violations = %d, want 0: %+v", n, res.GetViolations())
	}
}

// §5b's split is "somebody stated the fact, rather than inferring it", and a
// person signing their name to a sentence is the clearest case of stating
// there is. A human assertion counted as inferred would put the one edge that
// can be asked about in the same bucket as the ones that cannot.
func TestAHumanAssertionCountsAsDeterministicRatherThanInferred(t *testing.T) {
	cli := dial(t, harness{})

	res := asserted(t, cli, &alchemyv1.AssertRequest{
		Entities: []*alchemyv1.Entity{acme(), emea()},
		Relations: []*alchemyv1.Relation{
			{From: "customer:acme", To: "region:emea", Type: "OPERATES_IN"},
			{From: "customer:acme", To: "region:emea", Type: "BILLED_IN"},
		},
		By: "dana@example.com",
	})

	if got := res.GetCounts().GetDeterministic(); got != 2 {
		t.Errorf("counts.deterministic = %d, want 2", got)
	}
	if got := res.GetCounts().GetInferred(); got != 0 {
		t.Errorf("counts.inferred = %d, want 0", got)
	}
	if got := res.GetCounts().GetEntities(); got != 2 {
		t.Errorf("counts.entities = %d, want 2", got)
	}
}

// A fact stated with a reason is a different thing to audit from one stated
// without, so the note travels on every record rather than on the envelope.
func TestAnAssertionsNoteLandsOnEveryRecord(t *testing.T) {
	cli := dial(t, harness{})

	res := asserted(t, cli, &alchemyv1.AssertRequest{
		Entities:  []*alchemyv1.Entity{acme(), emea()},
		Relations: []*alchemyv1.Relation{{From: "customer:acme", To: "region:emea", Type: "OPERATES_IN"}},
		By:        "dana@example.com",
		Note:      "confirmed on the renewal call",
	})

	for _, e := range res.GetEntities() {
		if got := e.GetAttributes().GetFields()["note"].GetStringValue(); got != "confirmed on the renewal call" {
			t.Errorf("entity %s note = %q", e.GetId(), got)
		}
	}
	for _, r := range res.GetRelations() {
		if got := r.GetAttributes().GetFields()["note"].GetStringValue(); got != "confirmed on the renewal call" {
			t.Errorf("relation %s note = %q", r.GetType(), got)
		}
	}
}

// The call is synchronous and there is nothing to poll, but the record is
// still a job: §6's traceability is not something an operation gets to opt out
// of because it was quick.
func TestAnAssertionsJobResolvesThroughGetJob(t *testing.T) {
	cli := dial(t, harness{})

	res := asserted(t, cli, &alchemyv1.AssertRequest{
		Entities: []*alchemyv1.Entity{acme()},
		By:       "dana@example.com",
	})

	if res.GetJob() == "" {
		t.Fatal("the result names no job; a graph nobody named cannot be traced back to anything")
	}
	j, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: res.GetJob()})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if j.GetState() != alchemyv1.JobState_JOB_STATE_SUCCEEDED {
		t.Errorf("state = %v, want SUCCEEDED; an assertion is finished the moment it returns", j.GetState())
	}
}

// A vocabulary the caller sent and this service cannot read is the caller's
// mistake, and saying so beats checking the graph against nothing and calling
// every type undeclared.
func TestAnAssertionUnderAnUnreadableOntologyIsRefused(t *testing.T) {
	cli := dial(t, harness{})

	_, err := cli.Assert(authed(context.Background()), &alchemyv1.AssertRequest{
		Entities: []*alchemyv1.Entity{acme()},
		By:       "dana@example.com",
		Ontology: `{"id": "crm"}`,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

// pkg/preflight is the reading of a result every writer was already doing, and
// two entities under one ID is the defect it calls data loss: a store writes
// one node and every edge naming it points at whichever arrived last. There is
// no field on Result for a defect, so the refusal is the only place the
// asserter can be told.
func TestAnAssertionThatWouldLoseARecordIsRefused(t *testing.T) {
	cli := dial(t, harness{})

	_, err := cli.Assert(authed(context.Background()), &alchemyv1.AssertRequest{
		Entities: []*alchemyv1.Entity{
			{Id: "customer:acme", Type: "Customer", Name: "Acme"},
			{Id: "customer:acme", Type: "Region", Name: "EMEA"},
		},
		By: "dana@example.com",
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}
