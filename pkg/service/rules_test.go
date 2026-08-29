package service_test

import (
	"context"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// §5c: `always` is the verb that earns its keep — "reviewing a thousand
// extractions one at a time is not a workflow anybody sustains; reviewing the
// twelve kinds of mistake in them is". The rule it produces is worth nothing
// unless the caller gets it back: §4 means this service holds no policy
// between jobs, so the caller keeps it and supplies it on the next job, and a
// rule that never leaves is one nobody can supply.
func TestAnAlwaysDecisionComesBackAsARule(t *testing.T) {
	cli := dial(t, harness{run: staticResult(alsoGuessed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{
		SourceIds: []string{src}, Ontology: "crm",
		Review: &alchemyv1.ReviewOptions{Enabled: true},
	})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	stream := attach(t, cli, j.GetId())
	for i := 0; i < 2; i++ {
		item, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv %d: %v", i, err)
		}
		verb := alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT
		if item.GetKind() == alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT {
			verb = alchemyv1.ReviewVerb_REVIEW_VERB_ALWAYS
		}
		if err := stream.Send(&alchemyv1.ReviewDecision{
			JobId: j.GetId(), ItemId: item.GetId(), Verb: verb, By: "dana",
			Note: "the contract wins over the schema for this shape",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)
	got, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(got.GetRules()) != 1 {
		t.Fatalf("rules = %d, want the one `always` produced", len(got.GetRules()))
	}
	rule := got.GetRules()[0]
	if rule.GetShape() == "" {
		t.Error("the rule has no shape, so it can never match anything")
	}
	// §5c: a rule without an origin is an unexplainable policy.
	if rule.GetFrom().GetBy() != "dana" || rule.GetFrom().GetNote() == "" {
		t.Errorf("rule origin = %+v; six months on, the only reading of a rule with no decision is that somebody must have had a reason", rule.GetFrom())
	}
	if rule.GetBecause() == "" {
		t.Error("the rule does not say what it was made from; the item is gone by the time anybody reads it")
	}
}
