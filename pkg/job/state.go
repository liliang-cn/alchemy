// Package job holds jobs and nothing else.
//
// §5c calls the service a print queue that never becomes a filesystem: a job
// under review is held until it is accepted or expires, and what is held is
// work in progress, never a knowledge base. Everything in this package follows
// from that sentence — it stores no graph, knows nothing about extraction, and
// imports no pipeline package.
package job

import (
	"errors"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// actor is who is asking for a state change, and it is half of the transition
// table rather than a detail of it.
//
// Without it the table can say a held job may reach SUCCEEDED but not that a
// *node* may not put it there — and that is the interesting half. §7.3 makes a
// conflict a question for a person; a worker that could answer it by writing
// SUCCEEDED would make review a convention rather than a rule.
type actor uint8

const (
	// actorWorker holds a live lease on the job. It is the only actor that can
	// move work forward, and it proves itself by presenting a Lease that only
	// Claim can mint.
	actorWorker actor = 1 << iota
	// actorCaller is the client or the reviewer: no lease, no claim on the
	// work, but the right to withdraw it or to answer the question it asked.
	actorCaller
	// actorSweeper is the store's own clock. It is separate from the caller
	// because expiry is not a decision anyone made, and an API that let a
	// client declare a job EXPIRED would lose that distinction.
	actorSweeper
)

func (a actor) String() string {
	switch a {
	case actorWorker:
		return "worker"
	case actorCaller:
		return "caller"
	case actorSweeper:
		return "sweeper"
	default:
		return fmt.Sprintf("actor(%d)", uint8(a))
	}
}

// legal is the whole state machine: from -> to -> the set of actors allowed to
// make that move. A pair absent from the table is refused, so adding a state to
// the contract without deciding its edges leaves those edges closed rather than
// open, which is the safe direction to be wrong in.
var legal = map[alchemy.JobState]map[alchemy.JobState]actor{
	alchemy.JobPending: {
		alchemy.JobRunning:   actorWorker,
		alchemy.JobCancelled: actorCaller,
		alchemy.JobExpired:   actorSweeper,
	},
	alchemy.JobRunning: {
		// The only self-transition, and it is the one §8.3 asks for: a lease
		// expired because a node was slow or dead, and another node took the
		// job over. Refusing it would mean a dead node takes the work with it.
		alchemy.JobRunning: actorWorker,
		// Released by its worker, or requeued by the sweeper when the lease
		// died: an idle cluster has nobody asking for work, so nothing would
		// otherwise notice that the node holding this job stopped answering.
		alchemy.JobPending:     actorWorker | actorSweeper,
		alchemy.JobNeedsReview: actorWorker,
		alchemy.JobSucceeded:   actorWorker,
		alchemy.JobFailed:      actorWorker,
		alchemy.JobCancelled:   actorCaller,
	},
	alchemy.JobNeedsReview: {
		alchemy.JobSucceeded: actorCaller,
		alchemy.JobFailed:    actorCaller,
		alchemy.JobCancelled: actorCaller,
		alchemy.JobExpired:   actorSweeper,
	},
	// The four terminal states have no entry at all. Absence is the statement:
	// a finished job is finished, and a re-delivered message from a node that
	// was partitioned when the job ended finds nothing to corrupt.
}

// terminal reports whether a state is an end. It reads the table rather than
// listing states, so the two can never drift apart.
func terminal(s alchemy.JobState) bool { return len(legal[s]) == 0 }

// ErrIllegalTransition is what every refused state change unwraps to, so a
// caller can tell "you asked for something the machine does not do" from "the
// store is broken" with one errors.Is.
var ErrIllegalTransition = errors.New("illegal job transition")

// TransitionError says what was refused and why in words an operator reading
// one log line can act on. The states and the actor are all in the message
// because the fields are only reachable by a caller who already suspected this
// error, and the operator reading the line did not.
type TransitionError struct {
	From, To alchemy.JobState
	By       actor
	Why      string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("job: %s -> %s refused for %s: %s", e.From, e.To, e.By, e.Why)
}

func (e *TransitionError) Unwrap() error { return ErrIllegalTransition }

// check is the single gate every state change in this package passes through.
// There is deliberately no exported way to write a state directly: a store
// method that set State itself would make the table advice rather than law.
func check(from, to alchemy.JobState, by actor) error {
	allowed, ok := legal[from][to]
	switch {
	case ok && allowed&by != 0:
		return nil
	case terminal(from):
		return &TransitionError{from, to, by, string(from) + " is terminal"}
	case !ok:
		return &TransitionError{from, to, by, "no such transition"}
	default:
		return &TransitionError{from, to, by, "only " + allowed.names() + " may"}
	}
}

// names renders an actor set for the refusal message.
func (a actor) names() string {
	var out []string
	for _, one := range []actor{actorWorker, actorCaller, actorSweeper} {
		if a&one != 0 {
			out = append(out, one.String())
		}
	}
	if len(out) == 0 {
		return "nobody"
	}
	return strings.Join(out, " or ")
}
