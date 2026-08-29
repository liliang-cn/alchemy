package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Sentinel errors. Each one exists because a caller has a different thing to
// do about it: retry later, retry never, or page someone.
var (
	// ErrNotFound — no such job. Also what a caller gets after Delete, and
	// after the retention sweep drops a finished job nobody collected.
	ErrNotFound = errors.New("job: not found")
	// ErrExists — Create was given an ID the store already holds. It is
	// returned *with* the stored job, so a retrying client can ignore it; see
	// Mem.Create for why that is the useful shape.
	ErrExists = errors.New("job: already exists")
	// ErrAtCapacity — the store is full. §8.4: this is the "try later" that
	// separates admission control from optimism, and it is a sentinel so that a
	// client's retry loop can tell it apart from a failure that retrying will
	// never fix.
	ErrAtCapacity = errors.New("job: store at capacity, try later")
	// ErrLeaseTooShort — a lease that is over the instant it is granted. It is
	// refused rather than clamped: a node asking for one has a bug, and a
	// store that quietly picked a duration for it would hide the bug behind
	// work that mysteriously runs twice.
	ErrLeaseTooShort = errors.New("job: lease must be longer than zero")
)

// CapacityError is the refusal with the numbers attached, for the operator who
// wants to know whether the queue is full because the limit is wrong or
// because the node is stuck. It unwraps to ErrAtCapacity so callers that only
// need "try later" do not have to know this type exists.
type CapacityError struct {
	// Capacity is the configured limit; Live is how many jobs are holding it.
	Capacity, Live int
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("job: store at capacity, try later (%d of %d live)", e.Live, e.Capacity)
}

func (e *CapacityError) Unwrap() error { return ErrAtCapacity }

// Config is the operator's half of this package. Every field is a number
// somebody will want to change on a machine we have never seen, so none of
// them is a constant in the code.
type Config struct {
	// Capacity is how many live jobs this store will hold. §8.4: a queue that
	// accepts everything is a queue that OOMs, and a rejected job is an
	// operator's problem for a minute where an accepted job that dies is their
	// problem for an afternoon.
	Capacity int
	// PendingTTL is how long queued work waits for a node before it is
	// abandoned. It exists so that a burst nobody had capacity for does not
	// become a queue that is still full tomorrow.
	PendingTTL time.Duration
	// ReviewTTL is how long a job held for optional review survives. §7.3:
	// optional review work can expire cheaply.
	ReviewTTL time.Duration
	// ConflictTTL is how long a job held on a conflict survives. §7.3: a job
	// blocked on a real question should outlive a long weekend, because
	// somebody has to be found. It still expires — §5c — but the timer respects
	// that finding them takes days, not hours.
	ConflictTTL time.Duration
	// DoneTTL is how long a finished job's state stays readable before the
	// store drops it. Without it the print queue becomes a filesystem by the
	// slowest possible route: callers that never call Delete.
	DoneTTL time.Duration
	// Clock is time. Nil means the wall clock.
	Clock Clock
}

// HoldReason is why a job stopped for a person, and it is a required argument
// rather than an inferred one because it picks which of the two expiry timers
// applies. §7.3 draws that line hard: an optional review and an unanswerable
// question are not the same kind of waiting.
type HoldReason uint8

const (
	// HoldReview — the caller asked for review mode. Cheap to expire: nobody
	// is blocked on the answer, the work was merely offered for inspection.
	HoldReview HoldReason = iota + 1
	// HoldConflict — two sources disagree and nothing in the data decides it.
	// §7.3: this holds the job whether or not review mode is on, so it can
	// arrive at an unattended pipeline, where the person who must answer it may
	// not look at a queue until Tuesday.
	HoldConflict
)

func (r HoldReason) String() string {
	switch r {
	case HoldReview:
		return "review"
	case HoldConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// Store is the shared job store. §8.3: in-memory for a single node — the
// default, and what a buyer evaluating the product runs — and one real
// implementation, Postgres, for a cluster.
//
// The methods are named for intents rather than for writes. That is the point:
// a general SetState would make the transition table advice, and every illegal
// move in this package is refused by construction before it is refused by the
// table.
type Store interface {
	// Create admits a job. An empty id is minted; a non-empty one is the
	// caller's, and creating twice under it is idempotent.
	Create(ctx context.Context, id string) (alchemy.Job, error)
	Get(ctx context.Context, id string) (alchemy.Job, error)
	// Cancel withdraws a job. It is the caller's move, not a node's, and it
	// works whether the job is queued, running or held.
	Cancel(ctx context.Context, id string) error

	// Claim takes the oldest claimable job for ttl. false means the queue is
	// empty, which is not an error.
	Claim(ctx context.Context, node string, ttl time.Duration) (Lease, bool, error)
	// Heartbeat renews a lease and reports the stage the work has reached.
	Heartbeat(ctx context.Context, l Lease, stage string) (Lease, error)
	// Transition moves a leased job to a state that needs no further
	// information. Hold and Fail carry the two that do.
	Transition(ctx context.Context, l Lease, to alchemy.JobState) error
	// Fail ends a job with the cause attached.
	Fail(ctx context.Context, l Lease, cause string) error
	// Release hands unfinished work back to the queue.
	Release(ctx context.Context, l Lease) error
	// Hold stops a job for a person; the reason picks which expiry applies.
	Hold(ctx context.Context, l Lease, why HoldReason) error
	// Resolve is a person answering a held job. It takes no lease because a
	// reviewer is not a node.
	Resolve(ctx context.Context, id string, to alchemy.JobState) error

	// Expire runs one pass of the sweeper against the store's own clock.
	Expire(ctx context.Context) (Swept, error)
	Delete(ctx context.Context, id string) error
}
