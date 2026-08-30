package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"

	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// stated is the day the policy below was written down. §5c's "six months on"
// is a question nobody can ask of an undated rule.
var stated = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// authoredRule is §6's own sentence as a job input: "--flag is never an
// entity", declared by a person before this service ever saw the corpus.
func authoredRule() *alchemyv1.ReviewRule {
	return &alchemyv1.ReviewRule{
		Shape:  "violation/unknown_entity_type/type=Flag/producer=llm-extract/model=fake-llm",
		Kind:   alchemyv1.ReviewKind_REVIEW_KIND_VIOLATION,
		Origin: alchemyv1.RuleOrigin_RULE_ORIGIN_AUTHORED,
		From: &alchemyv1.ReviewDecision{
			Verb: alchemyv1.ReviewVerb_REVIEW_VERB_REJECT,
			By:   "ana@example.com",
			At:   timestamppb.New(stated),
		},
		Because: "--verbose is a command-line switch, not an entity",
	}
}

// A rule a caller states on the job reaches the run as one, with its origin
// intact. Without the origin the service would be told a person had looked at
// a finding when nobody had, which is the one thing the two kinds of rule
// differ about.
func TestAnAuthoredRuleReachesTheRunAsAuthored(t *testing.T) {
	specs := make(chan service.JobSpec, 1)
	cli := dial(t, harness{run: func(_ context.Context, _ string, spec service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		specs <- spec
		return alchemy.Result{}, nil
	}})
	src := upload(t, cli, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("--verbose is a Flag."))
	create(t, cli, &alchemyv1.CreateJobRequest{
		SourceIds: []string{src}, Ontology: "sds",
		Review: &alchemyv1.ReviewOptions{Rules: []*alchemyv1.ReviewRule{authoredRule()}},
	})

	spec := <-specs
	if len(spec.Review.Rules) != 1 {
		t.Fatalf("rules = %+v, want the one the caller stated", spec.Review.Rules)
	}
	got := spec.Review.Rules[0]
	if !got.Authored() {
		t.Errorf("rule = %+v, want it to reach the run as authored", got)
	}
	if got.From.By != "ana@example.com" || got.Because == "" || got.From.At.IsZero() {
		t.Errorf("rule = %+v, want the author, the reason and the date it was declared", got)
	}
}

// A rule that cannot explain itself is refused when the job is created, not
// when the first chunk meets it. §5c's argument is that an unexplainable
// policy is the failure; a job that accepted one and then applied it would
// have taken the policy into the graph before anybody could object.
func TestARuleThatCannotExplainItselfIsRefusedAtCreate(t *testing.T) {
	unexplained := authoredRule()
	unexplained.Because = ""

	conflicting := authoredRule()
	conflicting.Shape = "conflict/entity_type/between=ddl|llm-extract/model=fake-llm"
	conflicting.Kind = alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT
	conflicting.From.Verb = alchemyv1.ReviewVerb_REVIEW_VERB_ALWAYS

	tooWide := authoredRule()
	tooWide.Shape = "low_confidence/relation/type=/producer=llm-extract/model=fake-llm"

	for name, rule := range map[string]*alchemyv1.ReviewRule{
		"no stated reason":               unexplained,
		"a conflict answered in advance": conflicting,
		"a shape that covers everything": tooWide,
	} {
		t.Run(name, func(t *testing.T) {
			cli := dial(t, harness{})
			src := upload(t, cli, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
			_, err := cli.CreateJob(authed(context.Background()), &alchemyv1.CreateJobRequest{
				SourceIds: []string{src}, Ontology: "sds",
				Review: &alchemyv1.ReviewOptions{Rules: []*alchemyv1.ReviewRule{rule}},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("CreateJob = %v, want InvalidArgument: the rule is %s", err, name)
			}
		})
	}
}
