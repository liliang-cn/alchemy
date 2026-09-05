package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// leaseTTL is long because this node does not compete for its own jobs. §8.3's
// takeover exists for a cluster, where a lease that dies is how another node
// learns the holder did; on one node the only effect of a short lease would be
// a job being handed to a goroutine that is already running it.
const leaseTTL = 24 * time.Hour

// jobRun is one job's live state: what it was asked to do, who is listening,
// and — once there is one — the pending result.
//
// The result lives here rather than in the job store because pkg/job holds
// jobs and nothing else, deliberately. §5c is the rule this obeys: what is
// held is the pending result and the decisions made on it, never a knowledge
// base, and it is dropped the moment the job is.
type jobRun struct {
	spec JobSpec
	hub  *hub

	mu     sync.Mutex
	cancel context.CancelFunc
	result alchemy.Result
	// rules are what this job's `always` decisions produced. They are kept
	// beside the result because §5c records a rule with the decision that made
	// it, and the caller has to be handed them: the service keeps no policy
	// between jobs (§4), so a rule that never leaves is a rule nobody can
	// supply to the next one.
	rules []review.Rule
	// ready is set when result is the finished graph. A held job has one too:
	// §5c holds the *pending* result, which is exactly what a reviewer is
	// deciding about.
	ready bool
	// embedded says the vectors this job's release owed have been bought. It
	// is a claim rather than a report — see claimEmbed.
	embedded bool
}

func (r *jobRun) setCancel(c context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = c
}

func (r *jobRun) stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *jobRun) setResult(res alchemy.Result, rules []review.Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result, r.rules, r.ready = res, rules, true
}

func (r *jobRun) pending() (alchemy.Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.ready
}

// claimEmbed takes the right to spend this job's vectors, once.
//
// resolve runs on every decision and is reached from two RPCs at once — the
// Review stream and Decide — so two reviewers answering the last two questions
// together would both see an unheld job with no vectors and both pay for the
// same embedding. §8.2 draws exactly this line: "paying twice for the
// identical call after a crash is a bug", and paying twice because two people
// clicked at the same moment is the same bug with a friendlier cause.
//
// It is not released on failure. An embedding that failed is reported on the
// result (see embedSurvivors) and the job finishes; a claim handed back would
// buy a second attempt on the next decision, on a job that has none left to
// make.
func (r *jobRun) claimEmbed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.embedded {
		return false
	}
	r.embedded = true
	return true
}

func (r *jobRun) recorded() []review.Rule {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rules
}

// register records a job's spec before it is admitted, so a worker that claims
// it always finds one. The ordering is the whole reason this is a method: a
// spec registered after Create is a spec a concurrent worker can miss.
func (s *Server) register(id string, spec JobSpec) *jobRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[id]; ok {
		return r
	}
	r := &jobRun{spec: spec, hub: newHub()}
	s.runs[id] = r
	return r
}

func (s *Server) runFor(id string) *jobRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func (s *Server) hubFor(id string) *hub {
	if r := s.runFor(id); r != nil {
		return r.hub
	}
	return nil
}

// work claims one job and runs it.
//
// One goroutine is started per admitted job, and it runs whichever job the
// store hands it rather than the one whose creation started it. That sounds
// like a bug and is the opposite: pkg/job's Claim is the queue, and taking the
// oldest claimable job is what keeps arrival order. The counts match — one
// goroutine per admitted job — so nothing is left unclaimed, and a job
// withdrawn before it was claimed simply leaves one goroutine to find an empty
// queue and exit.
func (s *Server) work() {
	defer s.wg.Done()
	lease, ok, err := s.store.Claim(s.ctx, s.node, leaseTTL)
	if err != nil || !ok {
		return
	}
	s.run(lease)
}

func (s *Server) run(lease job.Lease) {
	id := lease.Job.ID
	r := s.runFor(id)
	if r == nil {
		// A claimed job with no spec is a bug in this package, not a caller's
		// mistake, and failing it loudly is better than leaving work leased to
		// a goroutine that is about to return.
		_ = s.store.Fail(context.Background(), lease, "service: job has no spec")
		return
	}

	ctx, cancel := context.WithCancel(s.ctx)
	r.setCancel(cancel)
	defer cancel()

	events := make(chan Event, 64)
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		s.pump(ctx, r, lease, events)
	}()

	res, err := s.call(ctx, id, r, events)
	// The runner owns nothing on this channel once it returns, so closing here
	// is what lets the pump finish rather than leaving a goroutine parked on a
	// range that never ends.
	close(events)
	<-pumped

	s.finish(lease, r, res, err)
}

// call runs the pipeline and turns a panic into a failed job.
//
// The Runner is somebody else's code, and one badly shaped page must cost the
// job it was in and nothing else. Letting the panic through would take every
// other import on the node with it, which is precisely the "accepted job that
// dies is their problem for an afternoon" §8.4 argues against — except worse,
// because it is every job rather than one.
//
// The panic value becomes the failure cause rather than being logged and
// swallowed: a job that failed for reasons nobody can read is a job nobody can
// debug, and pkg/job refuses a failure with no cause for the same reason.
func (s *Server) call(ctx context.Context, id string, r *jobRun, events chan<- Event) (res alchemy.Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("runner panicked on job %s: %v", id, p)
		}
	}()
	return s.cfg.Runner.Run(ctx, id, r.spec, events, r.hub)
}

// pump forwards the runner's events, and turns the ones that say where the
// work has got to into a heartbeat.
//
// It drains until the channel is closed even after cancellation, because the
// alternative is a runner blocked forever on a send to a reader that gave up —
// the goroutine leak this package is required not to have.
func (s *Server) pump(ctx context.Context, r *jobRun, lease job.Lease, events <-chan Event) {
	for e := range events {
		if e.At.IsZero() {
			e.At = time.Now()
		}
		if e.Item != nil {
			// §7.3: a question reaches the queue when it is found, so a
			// reviewer can start on it while the rest of the import runs.
			r.hub.offer(*e.Item)
		}
		if e.Stage != "" {
			_, _ = s.store.Heartbeat(ctx, lease, e.Stage)
		}
		r.hub.publish(e)
	}
}

// finish decides what the result means. It is here and not in the Runner
// because §7.3 is not a pipeline's decision to make: a conflict holds the job
// whether or not review mode is on, and a runner that could return SUCCEEDED
// over an unanswered conflict would make that rule a convention.
func (s *Server) finish(lease job.Lease, r *jobRun, res alchemy.Result, err error) {
	// Deliberately not the run's context: the run's context is cancelled by
	// the time we are recording why it ended, and a store write that refused
	// to happen because the work stopped would leave the job RUNNING forever.
	ctx := context.Background()
	id := lease.Job.ID

	if err != nil {
		if errors.Is(err, context.Canceled) {
			// The caller withdrew the job or the process is stopping. Either
			// way the state was already written by whoever cancelled, and
			// overwriting it with FAILED would report a shutdown as a defect.
			r.hub.close()
			return
		}
		_ = s.store.Fail(ctx, lease, err.Error())
		s.publishState(r, id, Event{Message: err.Error()})
		r.hub.close()
		return
	}

	r.setResult(res, nil)
	items := review.Queue(reportOf(res), res, r.spec.Review)
	r.hub.offer(items...)

	switch {
	case len(res.Held()) > 0:
		// §7.3's one refusal to let a caller opt out of a person.
		_ = s.store.Hold(ctx, lease, job.HoldConflict)
	case r.spec.Review.Reviewing && len(open(items, nil)) > 0:
		_ = s.store.Hold(ctx, lease, job.HoldReview)
	default:
		_ = s.store.Transition(ctx, lease, alchemy.JobSucceeded)
	}

	// A decision may already have arrived while the job was running — §6's
	// whole reason for a bidirectional stream — so a held job is offered the
	// answers it already has before anyone is asked for more.
	_ = s.resolve(id, r)

	s.publishState(r, id, Event{Counts: res.Counts})
	// A held job keeps its hub: the reviewer it is waiting for has not
	// connected yet, and closing the queue would hold the job on a question
	// nobody can be shown.
	if j, err := s.store.Get(ctx, id); err == nil && j.State != alchemy.JobNeedsReview {
		r.hub.close()
	}
}

// publishState tells watchers where the job ended up, so a client watching a
// stream learns the outcome from the stream rather than from a poll it should
// not have needed.
func (s *Server) publishState(r *jobRun, id string, e Event) {
	j, err := s.store.Get(context.Background(), id)
	if err != nil {
		return
	}
	e.At = time.Now()
	e.Stage = j.Stage
	r.hub.publish(e)
}

// reportOf presents a finished result as the verifier's report, which is what
// pkg/review ranks. Nothing is recomputed: §5c says what is worth reviewing is
// already computed, and a service that re-verified the graph would be a second
// opinion with no provenance of its own.
// reportOf rebuilds the verifier's report from a finished result, so a queue
// can be built from a graph rather than from the pass that produced it.
//
// Duplicates were missing from it, and the omission was silent in the way that
// matters: they are computed, they are counted, they are returned on the
// result — and review.Queue reads them off THIS struct, so no duplicate could
// ever reach a reviewer through the service. §5c's whole merge path was
// unreachable over gRPC and over HTTP, and nothing failed, because a queue
// that is short does not look like a queue that is wrong.
//
// Found by running it: one company under two ids, three duplicates on the
// result, zero items in the queue.
func reportOf(res alchemy.Result) verify.Report {
	return verify.Report{
		Entities:   res.Entities,
		Relations:  res.Relations,
		Violations: res.Violations,
		Conflicts:  res.Conflicts,
		Duplicates: res.Duplicates,
		Counts:     res.Counts,
	}
}

// open is the queue a person still has to answer: not suppressed by a rule,
// and not already decided.
func open(items []review.Item, decided map[string]bool) []review.Item {
	var out []review.Item
	for _, it := range review.Open(items) {
		if decided[it.ID] {
			continue
		}
		out = append(out, it)
	}
	return out
}
