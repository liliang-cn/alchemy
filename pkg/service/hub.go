package service

import (
	"sync"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// hub is one job's two conversations: progress out to watchers, and the review
// exchange with whoever is working its queue.
//
// It exists because both are fan-outs over work that is still running, and the
// alternative — the runner holding the client's stream — would make a client
// that disconnects a job that stops. §6 chose gRPC so that a decision reaches
// an extraction that has not run yet; that promise is kept here rather than in
// any RPC method, because it must survive the reviewer's connection dropping
// and coming back.
type hub struct {
	mu sync.Mutex

	// watchers receive every event. The channels are owned by the hub and
	// closed by it, so a subscriber that stopped reading cannot wedge the run.
	watchers map[chan Event]struct{}
	// reviewers receive items as they are found.
	reviewers map[chan review.Item]struct{}

	// items is every question raised on this job, in the order raised. It is
	// kept rather than streamed-and-forgotten so a reviewer connecting after a
	// conflict was found still sees it: §7.3 holds the job for a person who may
	// not look at a queue until Tuesday, and a queue that only existed while
	// somebody was watching would hold the job forever.
	items []review.Item
	byID  map[string]int

	// decided is what the service has been told, by item. Deduplication lives
	// here because a bidirectional stream that reconnects redelivers, and a
	// redelivered answer is the same answer — not a second one.
	decided map[string]review.Decision
	order   []string

	// latest is the running picture: the newest stage, counts and cost. A
	// watcher that attaches at minute five of a two-hour import gets it
	// immediately, because "the job is at extract and has made 4,000 calls" is
	// the sentence §7.2 says an operator is deciding on, and making them wait
	// for the next tick to learn it is the poll gRPC was chosen to avoid.
	latest Event
	seen   bool
	// conflicts are replayed to every watcher that attaches. §7.3 wants a
	// conflict known in minute three; an operator who connected in minute four
	// still needs it, and a stream that only carried what happened while they
	// were looking would make that a matter of luck.
	conflicts []alchemy.Conflict

	closed bool
}

// watcherBuffer is per subscriber. It is generous because the cost of a slow
// watcher must land on the watcher and never on the import: dropping is how
// this hub refuses to let a client's read speed become the pipeline's.
const watcherBuffer = 256

func newHub() *hub {
	return &hub{
		watchers:  map[chan Event]struct{}{},
		reviewers: map[chan review.Item]struct{}{},
		byID:      map[string]int{},
		decided:   map[string]review.Decision{},
	}
}

// Decisions implements Inbox: every answer recorded so far, in arrival order.
func (h *hub) Decisions() []review.Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]review.Decision, 0, len(h.order))
	for _, id := range h.order {
		out = append(out, h.decided[id])
	}
	return out
}

// Rules implements Inbox: the `always` rules this job's answers have made so
// far, in the order they were made.
//
// The hub is where this can be answered at all, and that is why the method is
// here rather than in pkg/review or in the runner. §5c's rule is a decision
// plus the item it was made on — the shape it generalises over, and the
// sentence the reviewer was looking at — and the hub is the only thing holding
// both halves while the job is still running. pkg/review assembles the same
// pairing at the end of a job, from the same two halves; this is that pairing
// asked for early, which is the whole of §6's "a decision reaches an
// extraction that has not run yet".
//
// A decision on an item the hub has since forgotten produces no rule rather
// than a rule with an empty shape. Rule.Covers is equality on the shape and
// refuses an empty one, so such a rule would match nothing and would sit in
// provenance claiming a chunk was extracted under a policy that never applied.
func (h *hub) Rules() []review.Rule {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []review.Rule
	seen := map[string]bool{}
	for _, id := range h.order {
		d := h.decided[id]
		if d.Verb != review.VerbAlways {
			continue
		}
		i, known := h.byID[id]
		if !known {
			continue
		}
		it := h.items[i]
		if it.Shape == "" || seen[it.Shape] {
			continue
		}
		seen[it.Shape] = true
		out = append(out, review.Rule{Shape: it.Shape, Kind: it.Kind, From: d, Because: it.Summary})
	}
	return out
}

// watch subscribes to progress. The returned function unsubscribes and must be
// called, which is what keeps a disconnected client from leaking a channel the
// run would go on writing to.
func (h *hub) watch() (<-chan Event, func()) {
	h.mu.Lock()
	ch := make(chan Event, watcherBuffer+len(h.conflicts)+1)
	if h.seen {
		ch <- h.latest
	}
	for _, c := range h.conflicts {
		catchUp := h.latest
		catchUp.Conflict = &c
		ch <- catchUp
	}
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.watchers[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.watchers[ch]; ok {
			delete(h.watchers, ch)
			close(ch)
		}
	}
}

// spend is the running total as it stands, for a caller building an event the
// hub did not produce. §7.2 makes the running cost the number an operator is
// deciding on, and an event assembled elsewhere — the greeting a new watcher
// gets, the last word after the hub closes — must not answer that question
// with a zero it never measured.
func (h *hub) spend() (int64, []alchemy.ModelCall, alchemy.Counts) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latest.ModelCalls, h.latest.ByStage, h.latest.Counts
}

// publish sends an event to every watcher.
//
// A full subscriber loses its oldest event rather than blocking the caller.
// That is the right trade for progress: the newest stage and the newest
// running cost are what §7.2 says an operator is deciding on, and a watcher
// too slow to keep up is better served by the recent truth than by a queue of
// stale ticks. Nothing that must not be lost travels only this way — every
// conflict is also in the result (§7.3) and every item is also in the queue.
func (h *hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remember(e)
	// An event that says nothing about the spend means "unchanged", not "zero".
	// The distinction is the whole value of the stream: §7.2 gives it the one
	// job the result cannot do — report the bill while the job can still be
	// cancelled — so a watcher reads the running total as the truth so far and
	// keeps the last number it was sent. A terminal event that answered a
	// different question with an implicit zero walked that total backwards, and
	// a client that had simply kept the last value finished believing the job
	// was free. Events are partial for good reason (see remember); this is the
	// same merge, applied on the way out rather than only on the way in.
	e.ModelCalls = h.latest.ModelCalls
	if len(e.ByStage) == 0 {
		e.ByStage = h.latest.ByStage
	}
	if e.Counts == (alchemy.Counts{}) {
		e.Counts = h.latest.Counts
	}
	for ch := range h.watchers {
		select {
		case ch <- e:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
}

// remember folds an event into the running picture a late watcher is primed
// with. Callers hold h.mu.
//
// The merge is per field rather than wholesale because events are partial: a
// runner reporting a conflict is not also restating the stage, and a snapshot
// that took the last event whole would tell a late watcher the job had gone
// back to stage "".
func (h *hub) remember(e Event) {
	h.seen = true
	if e.Stage != "" {
		h.latest.Stage = e.Stage
	}
	if e.ModelCalls > h.latest.ModelCalls {
		h.latest.ModelCalls = e.ModelCalls
	}
	if len(e.ByStage) > 0 {
		h.latest.ByStage = e.ByStage
	}
	if e.Counts != (alchemy.Counts{}) {
		h.latest.Counts = e.Counts
	}
	h.latest.At = e.At
	h.latest.Conflict = nil
	if e.Conflict != nil {
		h.conflicts = append(h.conflicts, *e.Conflict)
	}
}

// review subscribes to the queue and is primed with every open item.
//
// Priming is the reconnection semantics, stated in one place: a reviewer who
// connects — first time or fifth — is sent the questions that are still open,
// and never one they have already answered. A decision is not echoed back as
// an item, so a client that reconnects sees a shorter queue rather than its
// own work returning as new.
func (h *hub) review() (<-chan review.Item, func()) {
	h.mu.Lock()
	open := h.openLocked()
	ch := make(chan review.Item, len(open)+watcherBuffer)
	for _, it := range open {
		ch <- it
	}
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.reviewers[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.reviewers[ch]; ok {
			delete(h.reviewers, ch)
			close(ch)
		}
	}
}

// openLocked is the queue minus what has been answered. Callers hold h.mu.
func (h *hub) openLocked() []review.Item {
	var out []review.Item
	for _, it := range h.items {
		if _, done := h.decided[it.ID]; done {
			continue
		}
		out = append(out, it)
	}
	return out
}

// offer adds a question, and sends it to anyone connected.
//
// An item whose ID is already known replaces the earlier one instead of
// arriving twice. The runner publishes a conflict the moment it finds it and
// the service publishes the finished queue at the end, and those are the same
// question asked twice — a reviewer shown both would be answering one finding
// under two entries, which is the queue nobody reads.
func (h *hub) offer(items ...review.Item) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, it := range items {
		if i, seen := h.byID[it.ID]; seen {
			h.items[i] = it
			continue
		}
		h.byID[it.ID] = len(h.items)
		h.items = append(h.items, it)
		if _, done := h.decided[it.ID]; done {
			continue
		}
		for ch := range h.reviewers {
			select {
			case ch <- it:
			default:
				// A reviewer this far behind is not reading; the item stays in
				// the queue and reaches them when they reconnect.
			}
		}
	}
}

// items known to the hub, in the order they were raised.
func (h *hub) queue() []review.Item {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]review.Item(nil), h.items...)
}

// record takes a reviewer's answer.
//
// A redelivered answer is accepted silently and a contradictory one is
// refused. That asymmetry is the whole of the reconnection contract: a stream
// that reconnects redelivers, so treating a resend as a second decision would
// make reconnection change the graph — while two different answers to one
// question have no later one to prefer (pkg/review says so), and picking one
// would be the "whichever edge was written last" failure §7.3 exists to
// prevent.
func (h *hub) record(d review.Decision) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	prior, seen := h.decided[d.ItemID]
	if seen {
		if sameDecision(prior, d) {
			return nil
		}
		return invalid("review: item %q already has a different decision (%s by %s); decisions are a set, so there is no later one to prefer",
			d.ItemID, prior.Verb, prior.By)
	}
	h.decided[d.ItemID] = d
	h.order = append(h.order, d.ItemID)
	return nil
}

// retract removes an answer. It exists for one case: a decision that only
// turns out to be impossible once it is applied alongside the others. Leaving
// it recorded would hold the job forever on a set that can never be applied,
// and the reviewer who could correct it is the one being handed the error.
func (h *hub) retract(itemID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.decided[itemID]; !ok {
		return
	}
	delete(h.decided, itemID)
	for i, id := range h.order {
		if id == itemID {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}

// sameDecision is the redelivery test. It is deliberately over every field a
// reviewer can set: an answer that differs anywhere is a different answer, and
// guessing which differences do not matter is how a resend quietly overwrites
// a note somebody wrote.
func sameDecision(a, b review.Decision) bool {
	if a.ItemID != b.ItemID || a.Verb != b.Verb || a.By != b.By || a.Note != b.Note {
		return false
	}
	switch {
	case a.Edit == nil && b.Edit == nil:
		return true
	case a.Edit == nil || b.Edit == nil:
		return false
	default:
		return *a.Edit == *b.Edit
	}
}

// close ends every subscription. A watcher blocked on a finished job's channel
// is the goroutine leak this method exists to make impossible.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.watchers {
		delete(h.watchers, ch)
		close(ch)
	}
	for ch := range h.reviewers {
		delete(h.reviewers, ch)
		close(ch)
	}
}
