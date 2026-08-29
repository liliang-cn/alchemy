package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// ErrLeaseLost is what a node gets when it writes to a job it no longer owns.
// §8.3 asks for exactly this and not for prevention: a lease that expires
// because a node was merely slow means two nodes briefly work the same job,
// and that must be survivable rather than prevented, because preventing it is
// the part that needs consensus.
var ErrLeaseLost = errors.New("job: lease lost")

// LeaseError says who does hold the job, because the interesting question when
// a write is refused is not "was I refused" but "was I overtaken, or was the
// job cancelled underneath me".
type LeaseError struct {
	JobID string
	// Node is who asked; Holder is who the store says owns the job now, empty
	// when nobody does.
	Node, Holder string
	State        alchemy.JobState
}

func (e *LeaseError) Error() string {
	holder := e.Holder
	if holder == "" {
		holder = "nobody"
	}
	return fmt.Sprintf("job %s: %q wrote under a lost lease; held by %s, state %s",
		e.JobID, e.Node, holder, e.State)
}

func (e *LeaseError) Unwrap() error { return ErrLeaseLost }

// Lease is a node's evidence that it owns a job.
//
// token is unexported deliberately. It means a Lease cannot be written down as
// a struct literal — the only way to hold one is to have been given it by
// Claim or Heartbeat — so "write only under a lease you were granted" is
// enforced by the compiler for every caller outside this package, and the
// runtime check below is the second line rather than the only one.
//
// The token is also what makes two nodes on the same job harmless. It rises on
// every claim, so a node whose lease was taken over is not merely late: its
// token is a number the store has moved past, and no write it makes can land.
//
// The consequence, which is deliberate: only this package can mint a Lease, so
// the Postgres store of §8.3 belongs here beside Mem rather than in a package
// of its own. The alternative is an exported constructor, and a constructor
// that lets a real implementation mint a token also lets a worker mint one.
type Lease struct {
	Job      alchemy.Job
	Node     string
	Deadline time.Time

	token uint64
}

// Claim hands a node one job to work on, or reports that there is none.
//
// An empty queue is (Lease{}, false, nil) rather than an error: a worker loop
// polling an idle store is the normal case, and a store that called it a
// failure would teach every operator to ignore the failure.
func (m *Mem) Claim(ctx context.Context, node string, ttl time.Duration) (Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, false, err
	}
	if ttl <= 0 {
		return Lease{}, false, ErrLeaseTooShort
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.cfg.Clock.Now()
	for _, id := range m.order {
		r := m.jobs[id]
		switch {
		case r.job.State == alchemy.JobPending:
		case r.job.State == alchemy.JobRunning && !now.Before(r.deadline):
			// A takeover. §8.3: the node that held this is dead or slow, and
			// the content-addressed cache means the re-run costs the chunks
			// that had not finished rather than all of them.
		default:
			continue
		}
		if err := check(r.job.State, alchemy.JobRunning, actorWorker); err != nil {
			return Lease{}, false, err
		}
		m.fence++
		r.token = m.fence
		r.node = node
		r.ttl = ttl
		r.deadline = now.Add(ttl)
		m.enter(r, alchemy.JobRunning)
		return Lease{Job: r.job, Node: node, Deadline: r.deadline, token: r.token}, true, nil
	}
	return Lease{}, false, nil
}

// held returns the record a lease still owns, or the error explaining why it
// does not. Callers hold m.mu.
//
// The lease deadline is deliberately not part of this test. A node whose lease
// aged out while nobody wanted the job is still the node doing the work, and
// killing its progress to honour a clock would be prevention where §8.3 asked
// for survivability. What decides ownership is the token: if another node took
// the job, the number moved, and this node's writes stop landing at that
// instant rather than at some agreed one.
func (m *Mem) held(l Lease) (*record, error) {
	r, ok := m.jobs[l.Job.ID]
	if !ok {
		return nil, ErrNotFound
	}
	if r.job.State != alchemy.JobRunning || r.node != l.Node || r.token != l.token || l.token == 0 {
		return nil, &LeaseError{JobID: l.Job.ID, Node: l.Node, Holder: r.node, State: r.job.State}
	}
	return r, nil
}

// Heartbeat renews a lease and reports where the work has got to.
//
// The two are one call because they are one fact: a node that can say which
// stage it is in is a node that is alive, and a separate progress call would
// be a second thing to forget to make.
func (m *Mem) Heartbeat(ctx context.Context, l Lease, stage string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	r, err := m.held(l)
	if err != nil {
		return Lease{}, err
	}
	if stage != "" {
		// An empty stage means "no news", not "no stage": the heartbeat loop
		// and the code that knows about stages are rarely the same goroutine.
		r.job.Stage = stage
	}
	// Renewed by the TTL the node asked for at Claim, not by one this package
	// picked: the node knows how long its chunks take and the store does not.
	r.deadline = m.cfg.Clock.Now().Add(r.ttl)
	r.job.ExpiresAt = r.deadline
	return Lease{Job: r.job, Node: l.Node, Deadline: r.deadline, token: r.token}, nil
}

// Transition moves a job forward under a lease.
//
// It refuses the two states that need more than a name. A NEEDS_REVIEW without
// a reason cannot pick between §7.3's two timers, and a FAILED without a cause
// is a job nobody can debug; both are mistakes that would only be found in
// production, so they are refused here rather than defaulted.
func (m *Mem) Transition(ctx context.Context, l Lease, to alchemy.JobState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch to {
	case alchemy.JobNeedsReview:
		return &TransitionError{alchemy.JobRunning, to, actorWorker,
			"a hold must say why, because the reason picks the expiry: use Hold"}
	case alchemy.JobFailed:
		return &TransitionError{alchemy.JobRunning, to, actorWorker,
			"a failure must say what went wrong: use Fail"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.held(l)
	if err != nil {
		return err
	}
	if err := check(r.job.State, to, actorWorker); err != nil {
		return err
	}
	m.enter(r, to)
	return nil
}

// Fail ends a job with the reason attached.
func (m *Mem) Fail(ctx context.Context, l Lease, cause string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cause == "" {
		return &TransitionError{alchemy.JobRunning, alchemy.JobFailed, actorWorker,
			"a failure must say what went wrong"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.held(l)
	if err != nil {
		return err
	}
	if err := check(r.job.State, alchemy.JobFailed, actorWorker); err != nil {
		return err
	}
	r.job.Error = cause
	m.enter(r, alchemy.JobFailed)
	return nil
}

// Release hands unfinished work back to the queue — a node shutting down
// cleanly, or one that decided it is the wrong node for this job. It is the
// polite version of the lease simply dying.
func (m *Mem) Release(ctx context.Context, l Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.held(l)
	if err != nil {
		return err
	}
	if err := check(r.job.State, alchemy.JobPending, actorWorker); err != nil {
		return err
	}
	m.enter(r, alchemy.JobPending)
	return nil
}
