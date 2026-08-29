package job

import (
	"context"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Swept is what one pass of the sweeper did, split by what it means rather
// than merged into a list of IDs.
//
// The three are not interchangeable and an operator's response to each is
// different: an expiry is work that was lost because nobody came, a requeue is
// work being retried after a node stopped answering, and a reap is a finished
// job nobody collected. A single []string would leave every reader of the
// sweeper's log guessing which of those happened.
type Swept struct {
	// Expired: queued or held work whose timer ran out. §5c's obligation —
	// this is the list that keeps the service from quietly growing a database
	// of abandoned reviews.
	Expired []string
	// Requeued: a lease died and the job went back to the queue. §8.3: a node
	// that dies mid-job must not take a held job with it.
	Requeued []string
	// Reaped: finished jobs dropped after DoneTTL, collected or not.
	Reaped []string
}

// Expire runs one pass of the sweeper.
//
// It takes no time argument. The store already has a clock, and a method that
// accepted a second opinion about what time it is would let a caller expire
// work early by passing a future instant, or keep it alive by passing a past
// one — two sources of truth for the one number that decides whether work
// still exists.
//
// It is a method rather than a goroutine because who runs it is the operator's
// decision: a single node ticks it, a cluster lets one node tick it or all of
// them, and either is correct — every decision it makes is a checked
// transition, so a second sweeper finds nothing left to do.
func (m *Mem) Expire(ctx context.Context) (Swept, error) {
	if err := ctx.Err(); err != nil {
		return Swept{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.cfg.Clock.Now()
	var out Swept
	// A copy, because reaping mutates m.order underneath the loop.
	ids := append([]string(nil), m.order...)
	for _, id := range ids {
		r := m.jobs[id]
		switch {
		case r.job.State == alchemy.JobRunning:
			if now.Before(r.deadline) {
				continue
			}
			if check(r.job.State, alchemy.JobPending, actorSweeper) != nil {
				continue
			}
			m.enter(r, alchemy.JobPending)
			out.Requeued = append(out.Requeued, id)
		case terminal(r.job.State):
			if now.Before(r.job.ExpiresAt) {
				continue
			}
			m.drop(id)
			out.Reaped = append(out.Reaped, id)
		default:
			if now.Before(r.job.ExpiresAt) {
				continue
			}
			if check(r.job.State, alchemy.JobExpired, actorSweeper) != nil {
				continue
			}
			m.enter(r, alchemy.JobExpired)
			out.Expired = append(out.Expired, id)
		}
	}
	return out, nil
}
