package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// §5c's `always` is the only decision that can reach work nobody has done yet:
// it is a decision about a *class*, and a class is the only thing a chunk
// nobody has extracted can be measured against. So the service has to be able
// to hand one over while the job is still running, and it has to be a whole
// rule — the shape it matches on and the sentence it was made from — not just
// the answer the reviewer sent.
//
// The runner here does what pkg/pipeline's extractor does: it raises a
// question, then asks its inbox until the standing answers change.
func TestAnAlwaysDecisionIsARuleWhileTheJobIsStillRunning(t *testing.T) {
	res := disputed()
	itemID := "conflict/entity_type/n1"
	const shape = "conflict/entity_type/between=ddl|llm-extract/model=gpt-x"
	got := make(chan []review.Rule, 1)

	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error) {
		events <- service.Event{Stage: "extract", Item: &review.Item{
			ID: itemID, Kind: review.KindConflict, Subject: "n1",
			Summary:    res.Conflicts[0].Detail,
			Shape:      shape,
			Provenance: res.Conflicts[0].Right.Provenance,
		}}
		for {
			if rules := in.Rules(); len(rules) > 0 {
				got <- rules
				return res, nil
			}
			select {
			case <-ctx.Done():
				return alchemy.Result{}, ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}})

	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})

	ctx, cancel := context.WithTimeout(authed(context.Background()), 5*time.Second)
	defer cancel()
	stream, err := cli.Review(ctx)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{JobId: j.GetId()}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	item, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{
		JobId: j.GetId(), ItemId: item.GetId(),
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ALWAYS, By: "dana",
		Note: "the schema wins over the contract for this shape",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case rules := <-got:
		if len(rules) != 1 {
			t.Fatalf("rules = %+v, want the one `always` produced", rules)
		}
		r := rules[0]
		// The shape is what Rule.Covers matches on. A rule without one matches
		// nothing, which is a policy that looks recorded and answers nothing.
		if r.Shape != shape {
			t.Errorf("rule shape = %q, want the item's %q", r.Shape, shape)
		}
		// §5c: a rule is recorded with the decision that produced it, and with
		// the sentence the reviewer was reading — the item is gone by the time
		// anybody reads the rule.
		if r.From.By != "dana" || r.From.Verb != review.VerbAlways || r.From.Note == "" {
			t.Errorf("rule origin = %+v; six months on this is a policy nobody can explain", r.From)
		}
		if r.Because == "" {
			t.Error("the rule does not say what it was made from")
		}
		if r.Kind != review.KindConflict {
			t.Errorf("rule kind = %q, want the item's", r.Kind)
		}
	case <-ctx.Done():
		t.Fatal("the runner never saw a rule: an `always` made while the job runs reached nothing that had not run yet")
	}

	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)
}

// A decision that is not `always` is an answer about one record, and must not
// become a policy about a class. §5c gives that power to exactly one verb.
func TestAPlainAcceptIsNotARule(t *testing.T) {
	res := disputed()
	itemID := "conflict/entity_type/n1"
	asked := make(chan int, 1)

	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error) {
		events <- service.Event{Stage: "extract", Item: &review.Item{
			ID: itemID, Kind: review.KindConflict, Subject: "n1",
			Summary:    res.Conflicts[0].Detail,
			Shape:      "conflict/entity_type/between=ddl|llm-extract/model=gpt-x",
			Provenance: res.Conflicts[0].Right.Provenance,
		}}
		for {
			if len(in.Decisions()) > 0 {
				asked <- len(in.Rules())
				return res, nil
			}
			select {
			case <-ctx.Done():
				return alchemy.Result{}, ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}})

	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})

	ctx, cancel := context.WithTimeout(authed(context.Background()), 5*time.Second)
	defer cancel()
	stream, err := cli.Review(ctx)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{JobId: j.GetId()}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	item, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{
		JobId: j.GetId(), ItemId: item.GetId(),
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case n := <-asked:
		if n != 0 {
			t.Fatalf("rules = %d after a plain accept; one record's answer became a policy about every record like it", n)
		}
	case <-ctx.Done():
		t.Fatal("the runner never saw the decision")
	}
}
