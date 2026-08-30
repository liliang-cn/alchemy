package service_test

import (
	"context"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The claim these tests exist for: a job held at NEEDS_REVIEW can be read and
// unblocked by a client that has only requests and responses. Everything below
// builds on review_test.go's fixtures, because the whole argument is that this
// is the same mechanism as the stream and not a second one.

// A held job's queue is readable as a list, and the list says what is holding
// it. Without the count a reader would have to work out which of the items are
// conflicts and which are findings the job is not waiting for.
func TestListingFindingsOfAHeldJobReturnsTheQueueAndWhatHoldsIt(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	got, err := cli.ListFindings(authed(context.Background()), &alchemyv1.ListFindingsRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if got.GetJobId() != j.GetId() {
		t.Errorf("job_id = %q, want %q", got.GetJobId(), j.GetId())
	}
	if got.GetState() != alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW {
		t.Errorf("state = %v, want NEEDS_REVIEW; the state is how a reader tells an empty queue from an unfinished job", got.GetState())
	}
	if len(got.GetItems()) != 1 {
		t.Fatalf("items = %d, want the one conflict", len(got.GetItems()))
	}
	it := got.GetItems()[0]
	if it.GetKind() != alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT {
		t.Errorf("kind = %v, want CONFLICT", it.GetKind())
	}
	if it.GetSubject() != "n1" {
		t.Errorf("subject = %q, want n1", it.GetSubject())
	}
	if it.GetProvenance().GetSource() != "deal.pdf" {
		t.Error("the item lost its provenance; a reviewer reading the list should see the PDF against the schema without a second call")
	}
	if got.GetHolding() != 1 {
		t.Errorf("holding = %d, want 1; the job is held by that conflict", got.GetHolding())
	}
}

// The whole claim of this endpoint, as a test: a batch of decisions arriving
// over one request unblocks a job, and the response says so without the caller
// re-listing to find out.
func TestDecidingTheLastConflictFinishesTheJob(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	res, err := cli.Decide(authed(context.Background()), &alchemyv1.DecideRequest{
		JobId: j.GetId(),
		Decisions: []*alchemyv1.ReviewDecision{{
			ItemId: items[0].GetId(),
			Verb:   alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT,
			By:     "dana",
			Note:   "the contract is newer than the schema",
		}},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.GetApplied() != 1 {
		t.Errorf("applied = %d, want 1", res.GetApplied())
	}
	if res.GetRemainingHolding() != 0 {
		t.Errorf("remaining_holding = %d, want 0; nothing is left unanswered", res.GetRemainingHolding())
	}
	if len(res.GetRejected()) != 0 {
		t.Errorf("rejected = %v, want none", res.GetRejected())
	}
	if res.GetState() != alchemyv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want SUCCEEDED; a batch that unblocks a job must say the job is unblocked", res.GetState())
	}

	// And the job really finished, rather than the response merely claiming it.
	if _, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()}); err != nil {
		t.Fatalf("GetResult after Decide: %v", err)
	}
}

// A batch is assembled from a list somebody read earlier, so an item that has
// since gone is reported and the decisions around it still land. Failing the
// whole call would throw away the reviewer's good work over one stale line.
func TestAnUnknownItemIsReportedAndTheRestOfTheBatchApplies(t *testing.T) {
	cli := dial(t, harness{run: staticResult(alsoGuessed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{
		SourceIds: []string{src}, Ontology: "crm",
		Review: &alchemyv1.ReviewOptions{Enabled: true},
	})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	batch := []*alchemyv1.ReviewDecision{{
		ItemId: "conflict/entity_type/ghost",
		Verb:   alchemyv1.ReviewVerb_REVIEW_VERB_REJECT,
		By:     "dana",
	}}
	for _, it := range findings(t, cli, j.GetId()) {
		batch = append(batch, &alchemyv1.ReviewDecision{
			ItemId: it.GetId(), Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana",
		})
	}

	res, err := cli.Decide(authed(context.Background()), &alchemyv1.DecideRequest{JobId: j.GetId(), Decisions: batch})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(res.GetRejected()) != 1 {
		t.Fatalf("rejected = %v, want the one unknown item", res.GetRejected())
	}
	if got := res.GetRejected()[0].GetItemId(); got != "conflict/entity_type/ghost" {
		t.Errorf("rejected item_id = %q, want the item that is not in the queue", got)
	}
	if res.GetRejected()[0].GetReason() == "" {
		t.Error("the rejection says nothing; a caller cannot tell a stale item from a typo without a reason")
	}
	if want := int32(len(batch) - 1); res.GetApplied() != want {
		t.Errorf("applied = %d, want %d; the decisions around the stale one still count", res.GetApplied(), want)
	}
	if res.GetState() != alchemyv1.JobState_JOB_STATE_SUCCEEDED {
		t.Errorf("state = %v, want SUCCEEDED; every real question was answered", res.GetState())
	}
	if res.GetRemainingHolding() != 0 {
		t.Errorf("remaining_holding = %d, want 0", res.GetRemainingHolding())
	}
}

// An unsigned decision is a client bug rather than a stale line, so it fails
// the call instead of being reported: the caller has to fix their request, and
// a batch half of which was silently dropped is how they would not notice.
func TestAnUnsignedDecisionFailsTheWholeBatch(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	_, err := cli.Decide(authed(context.Background()), &alchemyv1.DecideRequest{
		JobId: j.GetId(),
		Decisions: []*alchemyv1.ReviewDecision{
			{ItemId: items[0].GetId(), Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT},
		},
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", got, err)
	}

	j2, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if j2.GetState() != alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW {
		t.Errorf("state = %v, want the job still held; a refused batch must not have moved it", j2.GetState())
	}
}

// A job nobody has is not a job with nothing to review. An empty list would
// read as "all clear" for a job that never existed.
func TestListingFindingsOfAnUnknownJobIsNotFound(t *testing.T) {
	cli := dial(t, harness{})
	_, err := cli.ListFindings(authed(context.Background()), &alchemyv1.ListFindingsRequest{JobId: "nope"})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %v, want NotFound (err %v)", got, err)
	}
}

func TestDecidingWithoutAJobIsInvalid(t *testing.T) {
	cli := dial(t, harness{})
	_, err := cli.Decide(authed(context.Background()), &alchemyv1.DecideRequest{
		Decisions: []*alchemyv1.ReviewDecision{{ItemId: "x", Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana"}},
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

// A batch is about one job. A decision naming a different one is a caller who
// has mixed two queues together, and applying it to the batch's job would
// record an answer about a graph nobody was looking at.
func TestABatchNamingASecondJobIsRefused(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	_, err := cli.Decide(authed(context.Background()), &alchemyv1.DecideRequest{
		JobId: j.GetId(),
		Decisions: []*alchemyv1.ReviewDecision{{
			JobId: "some-other-job", ItemId: items[0].GetId(),
			Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana",
		}},
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (err %v)", got, err)
	}
}

// "One mechanism, two shapes" is only true if it is true of what ends up in
// the graph. Two identical jobs are reviewed, one over the stream and one over
// the batch, and the provenance they leave behind is compared.
func TestABatchedDecisionLandsInProvenanceLikeAStreamedOne(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))

	streamed := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm", IdempotencyKey: "streamed"})
	awaitState(t, cli, streamed.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)
	stream := attach(t, cli, streamed.GetId())
	item, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{
		JobId: streamed.GetId(), ItemId: item.GetId(),
		Verb: alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana", Note: "the contract is newer",
	}); err != nil {
		t.Fatalf("send decision: %v", err)
	}
	awaitState(t, cli, streamed.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	batched := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm", IdempotencyKey: "batched"})
	awaitState(t, cli, batched.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)
	listed := findings(t, cli, batched.GetId())
	if listed[0].GetId() != item.GetId() {
		t.Fatalf("the two shapes disagree about the item: listed %q, streamed %q", listed[0].GetId(), item.GetId())
	}
	if _, err := cli.Decide(authed(context.Background()), &alchemyv1.DecideRequest{
		JobId: batched.GetId(),
		Decisions: []*alchemyv1.ReviewDecision{{
			ItemId: listed[0].GetId(),
			Verb:   alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT, By: "dana", Note: "the contract is newer",
		}},
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	a := conflictProvenance(t, cli, streamed.GetId())
	b := conflictProvenance(t, cli, batched.GetId())
	if a.GetReviewedBy() != "dana" {
		t.Fatalf("streamed reviewed_by = %q, want dana", a.GetReviewedBy())
	}
	if b.GetReviewedBy() != a.GetReviewedBy() {
		t.Errorf("batched reviewed_by = %q, streamed = %q; the two shapes must be one mechanism", b.GetReviewedBy(), a.GetReviewedBy())
	}
	if b.GetProducer() != a.GetProducer() {
		t.Errorf("batched producer = %v, streamed = %v; review adds to provenance either way", b.GetProducer(), a.GetProducer())
	}
	if b.GetSource() != a.GetSource() {
		t.Errorf("batched source = %q, streamed = %q; review does not overwrite where a claim came from", b.GetSource(), a.GetSource())
	}
}

func findings(t *testing.T, cli alchemyv1.AlchemyClient, id string) []*alchemyv1.ReviewItem {
	t.Helper()
	got, err := cli.ListFindings(authed(context.Background()), &alchemyv1.ListFindingsRequest{JobId: id})
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(got.GetItems()) == 0 {
		t.Fatalf("job %s has nothing to review", id)
	}
	return got.GetItems()
}

func conflictProvenance(t *testing.T, cli alchemyv1.AlchemyClient, id string) *alchemyv1.Provenance {
	t.Helper()
	res, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: id})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(res.GetConflicts()) != 1 {
		t.Fatalf("conflicts = %d, want the answered one still reported", len(res.GetConflicts()))
	}
	return res.GetConflicts()[0].GetRight().GetProvenance()
}
