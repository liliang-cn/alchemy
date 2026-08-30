package runner

import (
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
)

func policy(t *testing.T, shape, because string) review.Rule {
	t.Helper()
	r, err := review.Authorship{
		Shape: shape, Verb: review.VerbReject, By: "ana@example.com",
		Because: because, At: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
	}.Rule()
	if err != nil {
		t.Fatalf("Authorship.Rule: %v", err)
	}
	return r
}

// A rule set the process was started with is in force on a job that says
// nothing about rules. That is the whole of "a nightly pipeline can carry
// policy as configuration": the operator who deployed the service is the
// person who knows the corpus, and a policy they have to restate in every
// request is one that will be missing from the request that mattered.
func TestConfiguredRulesReachAJobThatSuppliedNone(t *testing.T) {
	rule := policy(t, "violation/unknown_entity_type/type=Flag/producer=llm-extract/model=fake-llm",
		"--flag is a command-line switch, not an entity")

	req, err := buildRequest(service.JobSpec{
		Sources: []service.Source{{ID: "s1", Kind: alchemy.SourceDDL, Path: "x"}},
	}, nil, []review.Rule{rule})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	got := req.Inbox.Rules()
	if len(got) != 1 || got[0].Shape != rule.Shape {
		t.Fatalf("rules = %+v, want the one the process was configured with", got)
	}
}

// The job's own rules are looked at first, and the order is the whole of what
// it decides: review takes the first rule that covers an item, so this is
// which rule the provenance credits when two say the same thing. A policy
// stated about this job is closer to the work than one stated about the
// deployment, and is the one a reader should be sent to first.
func TestAJobsOwnRulesAreCreditedBeforeTheProcessWideOnes(t *testing.T) {
	shape := "violation/unknown_entity_type/type=Flag/producer=llm-extract/model=fake-llm"
	perJob := policy(t, shape, "this corpus writes switches as entities")
	perProcess := policy(t, shape, "no corpus of ours has entities that start with two dashes")

	req, err := buildRequest(service.JobSpec{
		Sources: []service.Source{{ID: "s1", Kind: alchemy.SourceDDL, Path: "x"}},
		Review:  review.Options{Rules: []review.Rule{perJob}},
	}, nil, []review.Rule{perProcess})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	got := req.Inbox.Rules()
	if len(got) != 2 {
		t.Fatalf("rules = %+v, want both", got)
	}
	if got[0].Because != perJob.Because {
		t.Fatalf("first rule = %q, want the job's own", got[0].Because)
	}
}

// A configured rule that cannot explain itself stops the process being built,
// not the first job that meets it. It is the same refusal ErrNoFactory makes
// and for the same reason: the failure would otherwise arrive in the middle of
// the night, minutes into an import, on behalf of a person who has gone home.
func TestAConfiguredRuleThatCannotExplainItselfRefusesToBuildTheRunner(t *testing.T) {
	_, err := New(Config{Factory: &recordingFactory{}, Rules: []review.Rule{{
		Shape:  "violation/unknown_entity_type/type=Flag/producer=llm-extract",
		Kind:   review.KindViolation,
		Origin: review.OriginAuthored,
		From:   review.Decision{Verb: review.VerbReject, By: "ana"},
	}}})
	if err == nil {
		t.Fatal("New accepted a rule with no stated reason and no date")
	}
}
