package service

import (
	"context"
	"errors"
	"io"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/review"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
)

// Review is the conversation §6 chose gRPC for: items out, decisions in, on
// one connection, so a decision reaches an extraction that has not run yet.
//
// It is deliberately not gated on the job having asked for review. A conflict
// reaches NEEDS_REVIEW whether or not review mode is on (§7.3), and this is
// the only way it gets unblocked; refusing the stream for a job that did not
// opt in would leave an unattended pipeline holding a job it has no way to
// answer.
//
// The reconnection contract, in one place: on attaching, a reviewer is sent
// every item that is still open and none that they have answered, and a
// resent decision is recorded as the same decision rather than a second one.
// A decision is never echoed back as an item, so reconnecting shortens the
// queue instead of returning a reviewer's own work to them as new.
func (s *Server) Review(stream grpc.BidiStreamingServer[alchemyv1.ReviewDecision, alchemyv1.ReviewItem]) error {
	ctx := stream.Context()

	// The first message names the job. It may also carry a decision, so a
	// reviewer resuming a queue can answer and attach in one round trip.
	first, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return wireError(err)
	}
	id := first.GetJobId()
	if id == "" {
		return wireError(invalid("review: the first message must name the job whose queue this is"))
	}
	if _, err := s.store.Get(ctx, id); err != nil {
		return wireError(err)
	}
	r := s.runFor(id)
	if r == nil {
		return wireError(job.ErrNotFound)
	}

	items, unsubscribe := r.hub.review()
	defer unsubscribe()

	if err := s.decide(id, r, first); err != nil {
		return wireError(err)
	}

	// Receiving runs in its own goroutine because both directions are live at
	// once: a reviewer answering item three must not stop item four arriving,
	// which is the whole difference between this and a submit endpoint.
	failed := make(chan error, 1)
	go func() {
		defer close(failed)
		for {
			msg, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				// A half-close is a reviewer who has finished answering, not
				// one who has finished listening. The queue keeps flowing.
				return
			}
			if err != nil {
				return
			}
			if err := s.decide(id, r, msg); err != nil {
				failed <- err
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return wireError(ctx.Err())
		case err, ok := <-failed:
			if ok && err != nil {
				return wireError(err)
			}
			// The client half-closed. Nothing more will arrive on that side,
			// so stop selecting on it and go on delivering items.
			failed = nil
		case it, ok := <-items:
			if !ok {
				// The job was resolved, finished or deleted. There is nothing
				// left to ask, and the stream ends rather than idling.
				return nil
			}
			if err := stream.Send(itemToProto(id, it)); err != nil {
				return err
			}
		}
	}
}

// decide records one answer and tries to unblock the job with it.
//
// A message with no item is the attach handshake and decides nothing, which is
// what lets a reviewer name the job without pretending to have an opinion
// about the first thing in it.
func (s *Server) decide(id string, r *jobRun, msg *alchemyv1.ReviewDecision) error {
	if msg.GetItemId() == "" {
		return nil
	}
	d := decisionFromProto(msg)
	if err := s.vet(r, d); err != nil {
		return err
	}
	if err := r.hub.record(d); err != nil {
		return err
	}
	return s.resolve(id, r)
}

// vet refuses a decision the reviewer can be told about now rather than at the
// end. §5c's rules are pkg/review's to enforce, so the ones checked here are
// the two this layer can know without a finished result: the item has to be a
// question that was actually asked, and the answer has to be signed.
func (s *Server) vet(r *jobRun, d review.Decision) error {
	if d.By == "" {
		return invalid("review: the decision on item %q names nobody; a review with no reviewer is not a review", d.ItemID)
	}
	if d.Verb == "" {
		return invalid("review: the decision on item %q has no verb; accept, reject, edit or always", d.ItemID)
	}
	for _, it := range r.hub.queue() {
		if it.ID == d.ItemID {
			return nil
		}
	}
	return invalid("review: item %q is not in this job's queue", d.ItemID)
}

// resolve applies what has been decided so far and, if nothing is left open,
// finishes the job.
//
// It runs after every decision rather than on an explicit "done" message. A
// reviewer who answered the last question has finished by answering it, and
// making them say so as well is a step somebody forgets — leaving a job held
// on a queue with nothing in it until it expires.
func (s *Server) resolve(id string, r *jobRun) error {
	ctx := context.Background()
	j, err := s.store.Get(ctx, id)
	if err != nil || j.State != alchemy.JobNeedsReview {
		// Still running: the decision is in the inbox, which is where a
		// decision for work that has not run yet belongs.
		return nil
	}
	res, ok := r.pending()
	if !ok {
		return nil
	}

	items := r.hub.queue()
	decisions := r.hub.Decisions()
	out, rules, err := review.Apply(res, items, decisions)
	if err != nil {
		// The set is only invalid because of what just arrived, so it is
		// retracted: leaving it in would poison every later attempt to resolve
		// the job, and the reviewer who could fix it is the one being told.
		if len(decisions) > 0 {
			r.hub.retract(decisions[len(decisions)-1].ItemID)
		}
		return invalid("%s", err)
	}

	// A decision edits the graph, and the findings were computed before any
	// decision existed; see recheck. Anything new it turns up is a question
	// nobody has been asked, so it is added to the pending result and queued
	// from there.
	//
	// Onto the PENDING result and not onto the decided one, which is the whole
	// subtlety. review.Apply is called with the result the queue was built
	// from and refuses an item whose index it cannot resolve — "Apply needs
	// the result the queue was built from" is its own sentence, and it is
	// right: an item built from the merged graph and applied against the
	// unmerged one points at a conflict that is not there. So the pending
	// result grows the new questions, the queue is rebuilt from it, and the
	// two stay one document. Nothing else about it changes: the merge itself
	// lives in the decisions, which are re-applied from scratch every time, so
	// this stays the idempotent replay it has always been.
	if grown, added := s.discovered(r, res, out); added {
		res = grown
		r.setResult(res, r.recorded())
		items = review.Queue(reportOf(res), res, r.spec.Review)
		r.hub.offer(items...)
		out, rules, err = review.Apply(res, items, decisions)
		if err != nil {
			return invalid("%s", err)
		}
	}

	if len(out.Held()) > 0 {
		return nil // §7.3: a conflict nobody has put their name to still holds it.
	}
	decided := map[string]bool{}
	for _, d := range decisions {
		decided[d.ItemID] = true
	}
	if r.spec.Review.Reviewing && len(open(items, decided)) > 0 {
		return nil
	}

	r.setResult(out, rules)
	if err := s.store.Resolve(ctx, id, alchemy.JobSucceeded); err != nil {
		return err
	}
	s.publishState(r, id, Event{Counts: out.Counts})
	r.hub.close()
	return nil
}
