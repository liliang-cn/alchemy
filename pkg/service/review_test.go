package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// disputed is one result with one conflict in it: a schema and a contract
// disagreeing about what n1 is. §7.3 says that holds the job whether or not
// review mode is on, so every test below builds on this.
func disputed() alchemy.Result {
	schema := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	prose := alchemy.Provenance{Source: "deal.pdf", Chunk: 4, Producer: alchemy.ProducerLLMExtract, Model: "gpt-x", Confidence: 0.7}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "n1", Type: "Customer", Name: "Acme", Provenance: schema},
			{ID: "n1", Type: "Supplier", Name: "Acme", Provenance: prose},
		},
		Conflicts: []alchemy.Conflict{{
			Kind:    alchemy.ConflictEntityType,
			Subject: "n1",
			Detail:  `entity "n1" is typed "Customer" by schema.sql and "Supplier" by deal.pdf`,
			Left:    alchemy.Claim{Statement: "Customer", Provenance: schema},
			Right:   alchemy.Claim{Statement: "Supplier", Provenance: prose},
		}},
		Counts: alchemy.Counts{Entities: 2, Conflicts: 1},
	}
}

// §7.3's one refusal to let a caller opt out of a person: review mode is off
// here and the job still stops, and GetResult still will not hand the graph
// back as if it were finished.
func TestAConflictHoldsTheJobWithReviewOff(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})

	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	_, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("GetResult code = %v, want FailedPrecondition (err %v)", got, err)
	}
	if s := status.Convert(err).Message(); s == "" {
		t.Error("the refusal says nothing; a held job's caller needs to know what to do about it")
	}
}

// Items out and decisions in on one connection, and the decision unblocks the
// job — which is what makes Review the answer to §7.3 rather than a reporting
// endpoint.
func TestReviewDeliversItemsAndTakesDecisionsOnOneConnection(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

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
	if item.GetKind() != alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT {
		t.Errorf("kind = %v, want CONFLICT; §5c ranks conflicts above everything", item.GetKind())
	}
	if item.GetSubject() != "n1" {
		t.Errorf("subject = %q, want n1", item.GetSubject())
	}
	if item.GetProvenance().GetSource() != "deal.pdf" {
		t.Error("the item lost its provenance; a reviewer should see the PDF against the schema without looking anything up")
	}

	if err := stream.Send(&alchemyv1.ReviewDecision{
		JobId:  j.GetId(),
		ItemId: item.GetId(),
		Verb:   alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT,
		By:     "dana",
		Note:   "the contract is newer than the schema",
	}); err != nil {
		t.Fatalf("send decision: %v", err)
	}

	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	got, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(got.GetConflicts()) != 1 {
		t.Fatalf("conflicts = %d, want the answered one still reported", len(got.GetConflicts()))
	}
	// §5c: review adds to provenance, it does not overwrite it.
	c := got.GetConflicts()[0]
	if c.GetRight().GetProvenance().GetReviewedBy() != "dana" {
		t.Errorf("reviewed_by = %q, want dana", c.GetRight().GetProvenance().GetReviewedBy())
	}
	if c.GetRight().GetProvenance().GetProducer() != alchemyv1.Producer_PRODUCER_LLM_EXTRACT {
		t.Error("review overwrote the producer; a reviewed edge must still say a model proposed it")
	}
}

// §6's first reason for gRPC, tested: "a decision reaches an extraction that
// has not run yet". The runner will not finish until it has seen the decision,
// so a service that only handed decisions over at the end would deadlock here
// rather than merely be slower.
func TestADecisionReachesTheRunnerBeforeItFinishes(t *testing.T) {
	res := disputed()
	itemID := "conflict/entity_type/n1"
	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error) {
		events <- service.Event{Stage: "extract", Item: &review.Item{
			ID: itemID, Kind: review.KindConflict, Subject: "n1",
			Summary:    res.Conflicts[0].Detail,
			Provenance: res.Conflicts[0].Right.Provenance,
		}}
		// The chunk this stands in for has not run. A pipeline polls its inbox
		// between chunks; this one polls until the answer arrives.
		for {
			for _, d := range in.Decisions() {
				if d.ItemID == itemID {
					out := res
					out.Entities = append(append([]alchemy.Entity{}, res.Entities...), alchemy.Entity{
						ID: "learned", Type: "Note", Name: "decided-by-" + d.By,
						Provenance: alchemy.Provenance{Source: "deal.pdf", Chunk: 9, Producer: alchemy.ProducerLLMExtract},
					})
					return out, nil
				}
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
	if item.GetId() != itemID {
		t.Fatalf("item = %q, want the one the runner raised mid-import", item.GetId())
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{
		JobId: j.GetId(), ItemId: itemID,
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana",
	}); err != nil {
		t.Fatalf("send decision: %v", err)
	}

	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)
	got, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var learned bool
	for _, e := range got.GetEntities() {
		if e.GetName() == "decided-by-dana" {
			learned = true
		}
	}
	if !learned {
		t.Error("the runner finished without seeing the decision; §6's first reason for gRPC is not met")
	}
}

// Reconnection semantics, stated as a test: a reviewer who reconnects is sent
// the questions still open and never one they already answered, and a resent
// decision is the same decision rather than a second one.
func TestAReconnectingReviewerIsNotAskedWhatTheyAlreadyAnswered(t *testing.T) {
	srv, cli := serve(t, harness{run: staticResult(alsoGuessed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{
		SourceIds: []string{src}, Ontology: "crm",
		Review: &alchemyv1.ReviewOptions{Enabled: true},
	})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	first := attach(t, cli, j.GetId())
	conflict, err := first.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	decision := &alchemyv1.ReviewDecision{
		JobId: j.GetId(), ItemId: conflict.GetId(),
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana", Note: "checked against the contract",
	}
	if err := first.Send(decision); err != nil {
		t.Fatalf("send decision: %v", err)
	}
	waitFor(t, "the decision to be recorded", func() bool {
		return len(srv.DecisionsForTest(j.GetId())) == 1
	})

	// The connection drops, and the same reviewer comes back and resends.
	first.CloseSend()
	second := attach(t, cli, j.GetId())
	if err := second.Send(decision); err != nil {
		t.Fatalf("resend: %v", err)
	}

	seen, err := second.Recv()
	if err != nil {
		t.Fatalf("Recv on reconnect: %v", err)
	}
	if seen.GetId() == conflict.GetId() {
		t.Errorf("the answered item %q came back as a new question", seen.GetId())
	}
	if seen.GetKind() != alchemyv1.ReviewKind_REVIEW_KIND_GUESS {
		t.Errorf("kind = %v, want the still-open guess", seen.GetKind())
	}
	if n := len(srv.DecisionsForTest(j.GetId())); n != 1 {
		t.Errorf("decisions = %d, want 1; a redelivered answer is the same answer", n)
	}
}

// Two different answers to one question have no later one to prefer, so the
// second is refused rather than silently winning — which would be exactly the
// "whichever edge was written last" failure §7.3 exists to prevent.
//
// The job here has a second open question on purpose: answering the conflict
// would otherwise finish the job, and a stream that has ended is not a stream
// that refused anything.
func TestASecondDifferentDecisionIsRefused(t *testing.T) {
	cli := dial(t, harness{run: staticResult(alsoGuessed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm",
		Review: &alchemyv1.ReviewOptions{Enabled: true}})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	stream := attach(t, cli, j.GetId())
	item, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	_ = stream.Send(&alchemyv1.ReviewDecision{JobId: j.GetId(), ItemId: item.GetId(),
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana"})
	_ = stream.Send(&alchemyv1.ReviewDecision{JobId: j.GetId(), ItemId: item.GetId(),
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_REJECT, By: "sam"})

	for {
		_, err = stream.Recv()
		if err != nil {
			break
		}
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

// alsoGuessed is disputed() plus an independent question, for the tests that
// need the queue to stay open after one answer.
func alsoGuessed() alchemy.Result {
	res := disputed()
	res.Guesses = []alchemy.Guess{{
		Field: "cust_id", ChosenAs: "customer.id", Alternatives: []string{"order.customer_id"},
		Provenance: alchemy.Provenance{Source: "rows.csv", Chunk: -1, Producer: alchemy.ProducerTabular},
	}}
	res.Counts.Guesses = 1
	return res
}

// A decision nobody signed cannot be written into provenance, so it is refused
// at the door rather than at the end.
func TestAnUnsignedDecisionIsRefused(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	stream := attach(t, cli, j.GetId())
	item, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	_ = stream.Send(&alchemyv1.ReviewDecision{JobId: j.GetId(), ItemId: item.GetId(),
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT})
	for {
		_, err = stream.Recv()
		if err != nil {
			break
		}
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

func TestReviewOnAnUnknownJobIsNotFound(t *testing.T) {
	cli := dial(t, harness{})
	stream, err := cli.Review(authed(context.Background()))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	_ = stream.Send(&alchemyv1.ReviewDecision{JobId: "nope"})
	_, err = stream.Recv()
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %v, want NotFound (err %v)", got, err)
	}
}

func TestReviewWithoutAJobIsInvalid(t *testing.T) {
	cli := dial(t, harness{})
	stream, err := cli.Review(authed(context.Background()))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	_ = stream.Send(&alchemyv1.ReviewDecision{ItemId: "x", By: "dana"})
	_, err = stream.Recv()
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

// attach opens a review stream and names the job, which is the handshake the
// proto documents: a first message with a job and no item decides nothing.
func attach(t *testing.T, cli alchemyv1.AlchemyClient, id string) alchemyv1.Alchemy_ReviewClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(authed(context.Background()), 5*time.Second)
	t.Cleanup(cancel)
	stream, err := cli.Review(ctx)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{JobId: id}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return stream
}
