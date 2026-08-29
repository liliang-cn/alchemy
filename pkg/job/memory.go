package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Defaults. They are guesses, and §7.4 says so out loud about the held-job
// expiry in particular; they are written here so that a zero Config is a
// working store rather than one that expires everything instantly.
const (
	defaultCapacity    = 128
	defaultPendingTTL  = time.Hour
	defaultReviewTTL   = 24 * time.Hour
	defaultConflictTTL = 7 * 24 * time.Hour // a long weekend, and then some.
	defaultDoneTTL     = time.Hour
)

// record is a job plus the bookkeeping the shared contract deliberately does
// not carry. alchemy.Job is what a caller is shown; who holds the lease and
// until when is the store's business, and putting it in the wire type would
// invite a client to reason about another node's lease.
type record struct {
	job alchemy.Job
	// node is the current lease holder, empty when the job is not running.
	node string
	// token fences the lease. It rises on every claim, and every write from a
	// worker must present the current value — see Lease.
	token uint64
	// ttl is the lease length the holder asked for, remembered so a heartbeat
	// renews by the node's own number rather than one the store invented.
	ttl time.Duration
	// deadline is when the lease dies and the job becomes claimable again.
	deadline time.Time
	// hold is why the job stopped for a person, and so which of the two
	// expiry timers §7.3 asks for is counting down.
	hold HoldReason
}

// Mem is the in-memory Store: the single-node default of §8.3.
type Mem struct {
	cfg Config

	mu   sync.Mutex
	jobs map[string]*record
	// order preserves arrival order for Claim. A map iterated for the "next"
	// job hands out work in a random order, which turns a queue into a lottery
	// and lets one unlucky job sit behind newer ones indefinitely.
	order []string
	// fence is the source of every lease token, and it is store-wide rather
	// than per job because a job ID outlives the job: a client that retries
	// after its first job was collected and dropped gets a second, unrelated
	// job under the same name. A counter that restarted at 1 for each record
	// would hand the node still holding the first job's lease a token that is
	// valid for the second one.
	fence uint64
	// live counts jobs in a non-terminal state. It is a counter rather than a
	// scan because admission is on the hot path of a high-volume import, and
	// it is safe to keep one because every state change in this store goes
	// through exactly one function, enter.
	live int
}

// New returns a store. A zero Config is valid and means the defaults above.
func New(cfg Config) *Mem {
	if cfg.Capacity <= 0 {
		cfg.Capacity = defaultCapacity
	}
	if cfg.PendingTTL <= 0 {
		cfg.PendingTTL = defaultPendingTTL
	}
	if cfg.ReviewTTL <= 0 {
		cfg.ReviewTTL = defaultReviewTTL
	}
	if cfg.ConflictTTL <= 0 {
		cfg.ConflictTTL = defaultConflictTTL
	}
	if cfg.DoneTTL <= 0 {
		cfg.DoneTTL = defaultDoneTTL
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	return &Mem{cfg: cfg, jobs: map[string]*record{}}
}

var _ Store = (*Mem)(nil)

// mintID makes an unguessable job ID.
//
// It is random rather than sequential on purpose: a job ID is the only thing
// needed to read a job's state, and a counter tells every caller how many jobs
// the service has run and lets them ask about the one before theirs.
func mintID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a condition this package can sensibly
		// carry in every signature; a machine whose entropy source is broken
		// has a problem no job store can paper over.
		panic("job: cannot mint an ID: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Create admits a job.
//
// The caller may supply the ID, and doing so makes creation idempotent: a
// client whose call timed out cannot know whether the job was admitted, and
// both blind answers are expensive — retry and a 10GB dump is imported twice,
// give up and the night's work is silently lost. This is §8.3's at-least-once
// reasoning applied one step earlier than the writes it was written about.
//
// A repeat returns the stored job *and* ErrExists rather than one or the
// other. The error is what tells a caller who was not retrying that it has
// collided with somebody else's ID; the job is what lets a caller who was
// retrying carry on with a single errors.Is. Returning only the job would hide
// a real collision, and returning only an error would make the retry path a
// second round trip through Get.
func (m *Mem) Create(ctx context.Context, id string) (alchemy.Job, error) {
	if err := ctx.Err(); err != nil {
		return alchemy.Job{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if id != "" {
		if r, ok := m.jobs[id]; ok {
			// Deliberately before the capacity check and without touching the
			// deadlines: refusing a retry of work already admitted turns a
			// duplicate into a failure, and refreshing its expiry would let a
			// client that retries every minute hold a job open forever.
			return r.job, ErrExists
		}
	}
	if id == "" {
		id = mintID()
	}
	if m.live >= m.cfg.Capacity {
		return alchemy.Job{}, &CapacityError{Capacity: m.cfg.Capacity, Live: m.live}
	}
	now := m.cfg.Clock.Now()
	r := &record{job: alchemy.Job{
		ID:        id,
		State:     alchemy.JobPending,
		CreatedAt: now,
		ExpiresAt: now.Add(m.cfg.PendingTTL),
	}}
	m.jobs[id] = r
	m.order = append(m.order, id)
	m.live++
	return r.job, nil
}

// enter performs a state change that check has already approved. It is the
// only writer of Job.State in the package, which is what makes the live count
// trustworthy and what makes "every transition was checked" a property of the
// code rather than a review habit.
func (m *Mem) enter(r *record, to alchemy.JobState) {
	now := m.cfg.Clock.Now()
	was := r.job.State
	r.job.State = to
	if !terminal(was) && terminal(to) {
		m.live--
	}

	// ExpiresAt answers one question in every state: when does this stop being
	// true without anyone doing anything? That is a different sentence per
	// state, and reporting one number a caller can act on is worth more than
	// reserving the field for the single case §5c names.
	switch {
	case to == alchemy.JobPending:
		// The stage goes with the node. Leaving "extract" on a job nobody is
		// working is how a progress display lies.
		r.node, r.deadline, r.hold, r.job.Stage = "", time.Time{}, 0, ""
		r.job.ExpiresAt = now.Add(m.cfg.PendingTTL)
	case to == alchemy.JobRunning:
		// The lease deadline: after it the job is not discarded, it is offered
		// to another node.
		r.job.ExpiresAt = r.deadline
	case to == alchemy.JobNeedsReview:
		r.node, r.deadline = "", time.Time{}
		r.job.ExpiresAt = now.Add(m.holdTTL(r.hold))
	default:
		r.node, r.deadline, r.hold = "", time.Time{}, 0
		// A finished job is kept only long enough to be collected. §5c: the
		// print queue must not become a filesystem by the slow route either.
		r.job.ExpiresAt = now.Add(m.cfg.DoneTTL)
	}
}

// holdTTL is the whole of §7.3's second mechanic: optional review work can
// expire cheaply, a job blocked on a real question should outlive a long
// weekend, and neither of those is the other's timer.
func (m *Mem) holdTTL(r HoldReason) time.Duration {
	if r == HoldConflict {
		return m.cfg.ConflictTTL
	}
	return m.cfg.ReviewTTL
}

// Cancel withdraws a job on behalf of the caller. A running job can be
// cancelled: its worker finds out when its next write is refused, which is the
// same mechanism that protects the store from a node whose lease expired, so
// there is only one path to get wrong.
func (m *Mem) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if err := check(r.job.State, alchemy.JobCancelled, actorCaller); err != nil {
		return err
	}
	m.enter(r, alchemy.JobCancelled)
	return nil
}

func (m *Mem) Get(ctx context.Context, id string) (alchemy.Job, error) {
	if err := ctx.Err(); err != nil {
		return alchemy.Job{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.jobs[id]
	if !ok {
		return alchemy.Job{}, ErrNotFound
	}
	return r.job, nil
}

func (m *Mem) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if !terminal(r.job.State) {
		m.live--
	}
	m.drop(id)
	return nil
}

// drop removes a job and its place in the queue order. Callers hold m.mu.
func (m *Mem) drop(id string) {
	delete(m.jobs, id)
	for i, other := range m.order {
		if other == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}
