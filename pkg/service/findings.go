package service

import (
	"context"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// ListFindings is Review's queue for a job that has already stopped.
//
// It is not a weaker Review. The property Review's bidirectional shape buys is
// that a decision reaches an extraction that has not run yet, and a job held
// at NEEDS_REVIEW has no extraction left to reach: nothing is running, no
// further item will arrive, and the queue is a finite list in a finished
// result. Handing that list back as a list is what the case is, and forcing it
// through a stream was making every HTTP client pay for a property this case
// does not have — which is why a buyer could curl the graph and could not curl
// "this edge is wrong".
//
// The job's state travels with the items because the list alone cannot say why
// it is empty: a SUCCEEDED job with nothing to review and a RUNNING job that
// has not finished looking both have no findings, and a client that could not
// tell them apart would report all clear for a job still working.
func (s *Server) ListFindings(ctx context.Context, req *alchemyv1.ListFindingsRequest) (*alchemyv1.Findings, error) {
	id := req.GetJobId()
	if id == "" {
		return nil, wireError(invalid("list_findings: no job ID"))
	}
	j, err := s.store.Get(ctx, id)
	if err != nil {
		// NotFound rather than an empty list: "no such job" and "nothing to
		// review" are opposite answers, and a client that saw them as the same
		// message would report a job it never created as clean.
		return nil, wireError(err)
	}
	r := s.runFor(id)
	if r == nil {
		return nil, wireError(job.ErrNotFound)
	}

	items := r.hub.queue()
	out := &alchemyv1.Findings{
		JobId:   id,
		State:   jobStates[j.State],
		Holding: holding(r),
		Items:   make([]*alchemyv1.ReviewItem, 0, len(items)),
	}
	for _, it := range items {
		// The same converter the stream sends, so an item a reviewer read here
		// and an item they were sent there are the same item, down to the
		// provenance they judge it on.
		out.Items = append(out.Items, itemToProto(id, it))
	}
	return out, nil
}

// Decide submits a worked-through queue in one request.
//
// Every decision goes through s.decide, the path Review's stream uses, because
// the claim being made is that these are two arrival shapes of one mechanism
// and not two mechanisms that happen to agree today. A second implementation
// here would be free to drift — to vet differently, to record differently, to
// forget to try to unblock the job — and the drift would show up as a graph
// reviewed over HTTP that is not the graph the same answers produce over gRPC.
//
// Order is preserved for the reason the proto gives: two decisions about one
// item apply in the order given, which is what makes "reject, then always"
// mean what it reads as.
func (s *Server) Decide(ctx context.Context, req *alchemyv1.DecideRequest) (*alchemyv1.DecideResponse, error) {
	id := req.GetJobId()
	if id == "" {
		return nil, wireError(invalid("decide: no job ID; a batch of decisions is about one job"))
	}
	j, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, wireError(err)
	}
	r := s.runFor(id)
	if r == nil {
		return nil, wireError(job.ErrNotFound)
	}
	// A job that has stopped for good takes no decisions, and saying so is the
	// whole of this check.
	//
	// It reported success without it. hub.close does not empty the queue, so
	// vet still found the item, the hub still recorded the answer, and resolve
	// returned at its first line because the state is no longer NEEDS_REVIEW —
	// leaving the caller holding "applied: 1" for a decision that changed
	// nothing and will change nothing. A silent no-op that answers 200 is
	// worse than a refusal by exactly the margin that nobody goes looking for
	// it.
	//
	// PENDING and RUNNING are deliberately still accepted. §6's whole argument
	// for a bidirectional stream is that a decision reaches an extraction that
	// has not run yet, and this endpoint is the same mechanism arriving in a
	// different shape (see the rpc comment); refusing a decision because the
	// job is still working would be refusing the case the design is proudest
	// of.
	if j.State == alchemy.JobSucceeded || j.State == alchemy.JobFailed {
		return nil, wireError(wrongState(
			"job %s is %s, so its queue is closed and a decision would change nothing. A record in "+
				"a delivered graph is corrected by asserting the correction and naming what it "+
				"retires — POST /v1/assertions with supersedes — because §4 means this service "+
				"holds no graph to edit", id, j.State))
	}

	apply, rejected, err := s.sift(r, id, req.GetDecisions())
	if err != nil {
		// Nothing has been recorded yet: the whole batch is checked before any
		// of it is applied, so a caller whose request is malformed gets their
		// job back in the state they found it rather than half reviewed by a
		// call that returned an error.
		return nil, wireError(err)
	}

	out := &alchemyv1.DecideResponse{JobId: id, Rejected: rejected}
	for _, msg := range apply {
		if err := s.decide(id, r, msg); err != nil {
			// Reached when the batch contradicts itself — two different
			// answers to one item — which the hub refuses for the reason it
			// refuses them on the stream: there is no later one to prefer.
			return nil, wireError(err)
		}
		out.Applied++
	}

	// Re-read rather than remembered: s.decide resolves the job, so a batch
	// that answered the last open question has moved it to SUCCEEDED by now,
	// and reporting the state this call started with would tell the caller
	// their job is still waiting for them.
	j, err = s.store.Get(ctx, id)
	if err != nil {
		return nil, wireError(err)
	}
	out.State = jobStates[j.State]
	out.RemainingHolding = holding(r)
	return out, nil
}

// sift separates the decisions to apply from the ones to report back, and
// refuses the batch outright when the caller is wrong about something no queue
// could fix.
//
// The line is between a stale request and a broken one. A batch is assembled
// from a list somebody read ten minutes ago, so an item id the queue no longer
// has is an ordinary thing to be told about — a rule may have taken it away
// since — and failing the call over it would throw away every good decision
// beside it. A decision nobody signed, or one with no verb, is not stale: it
// could not be applied to any queue in any state of the world, and reporting
// it per-item would let a client ship a review loop that silently records
// nothing.
//
// The test uses s.vet, which is the stream's, so the two shapes cannot come to
// disagree about what a valid decision is. Whether the item is in the queue is
// what tells vet's three refusals apart, because vet checks the signature and
// the verb before it looks the item up.
func (s *Server) sift(r *jobRun, id string, msgs []*alchemyv1.ReviewDecision) ([]*alchemyv1.ReviewDecision, []*alchemyv1.DecisionRejection, error) {
	apply := make([]*alchemyv1.ReviewDecision, 0, len(msgs))
	var rejected []*alchemyv1.DecisionRejection
	for i, msg := range msgs {
		if other := msg.GetJobId(); other != "" && other != id {
			return nil, nil, invalid("decide: decision %d is about job %q but the batch is about job %q; a batch is one job's queue", i+1, other, id)
		}
		if msg.GetItemId() == "" {
			// On the stream this is the attach handshake, which names the job
			// without claiming an opinion about anything in it. A request that
			// already names the job in its own field has nothing to attach to,
			// so an item-less decision here is a caller who dropped a field.
			return nil, nil, invalid("decide: decision %d names no item", i+1)
		}
		if err := s.vet(r, decisionFromProto(msg)); err != nil {
			if !queued(r, msg.GetItemId()) {
				rejected = append(rejected, &alchemyv1.DecisionRejection{ItemId: msg.GetItemId(), Reason: err.Error()})
				continue
			}
			return nil, nil, err
		}
		apply = append(apply, msg)
	}
	return apply, rejected, nil
}

// queued reports whether the job was ever asked this question.
func queued(r *jobRun, itemID string) bool {
	for _, it := range r.hub.queue() {
		if it.ID == itemID {
			return true
		}
	}
	return false
}

// holding is how many conflicts are still keeping the job from finishing.
//
// It is counted off the result rather than off the queue because §7.3's rule
// is about the graph: a conflict stops holding a job when a name lands on one
// of its claims, and the queue does not know whose name or whether one landed.
// A job with no result yet — still pending, still running — is held by nothing
// at all, and the state beside this number is what tells that apart from a job
// whose queue has been worked through.
func holding(r *jobRun) int32 { return int32(unanswered(r)) }
