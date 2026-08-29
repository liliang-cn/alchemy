package job

import (
	"context"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Hold stops a job for a person.
//
// It takes the reason rather than deriving it, because the reason is the whole
// of §7.3's first mechanic: a job merely offered for optional review and a job
// blocked on a conflict are the same state with two different lifetimes, and a
// store that guessed between them would either throw away an unanswered
// question over a weekend or hoard reviews nobody wanted.
//
// The lease ends here. Nobody is working a held job — a person is — and
// keeping a node responsible for it would lose the job when that node
// restarts, which is the failure the lease existed to prevent.
func (m *Mem) Hold(ctx context.Context, l Lease, why HoldReason) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if why != HoldReview && why != HoldConflict {
		return &TransitionError{alchemy.JobRunning, alchemy.JobNeedsReview, actorWorker,
			"a hold must name a reason, because the reason picks the expiry"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.held(l)
	if err != nil {
		return err
	}
	if err := check(r.job.State, alchemy.JobNeedsReview, actorWorker); err != nil {
		return err
	}
	r.hold = why
	m.enter(r, alchemy.JobNeedsReview)
	return nil
}

// Resolve is a person answering the question the job asked.
//
// It takes no lease because the reviewer is not a node, and it is refused for
// any job that is not held: a caller reaching in to declare queued work
// SUCCEEDED is a bug, not a decision, and the message says which call they
// wanted instead.
func (m *Mem) Resolve(ctx context.Context, id string, to alchemy.JobState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if r.job.State != alchemy.JobNeedsReview {
		return &TransitionError{r.job.State, to, actorCaller,
			"only a job held for a person can be resolved; to withdraw work use Cancel"}
	}
	if err := check(r.job.State, to, actorCaller); err != nil {
		return err
	}
	m.enter(r, to)
	return nil
}
