package pipeline

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// HeldError is §7.3 as a type: "A job that finds a conflict does not finish.
// It reaches NEEDS_REVIEW and stays there until someone resolves it — whether
// or not the caller asked for review mode."
// It is also what review mode reaches when there is a queue and nobody has
// worked it, which is §5c's other sentence — "a job under review is held until
// it is accepted or expires". The two are one type because they are one state:
// the difference is only in what is being asked, and Queue says which.
//
// The graph is in Pending rather than in Run's first return value, and that is
// the whole reason this is a typed error rather than a field on the result. A
// caller cannot reach the held graph without naming the hold, so a job that
// needs a person cannot be mistaken for one that finished by a caller who
// forgot to check something.
type HeldError struct {
	// Conflicts are the questions nobody has answered. §7.3: these hold the
	// job whether or not review mode is on.
	Conflicts []alchemy.Conflict
	// Queue is what a person is being asked, conflicts first (§5c's ranking).
	// It is the queue less the items a recorded rule already answered and less
	// the ones this request brought a decision for.
	Queue []review.Item
	// Pending is the graph as it stands: complete except for the vectors,
	// which §5c will not spend until the text they describe has survived. It
	// carries the job's Counts and its ModelCalls, because a held job has
	// spent real money and §7.2 does not hide it.
	Pending alchemy.Result
}

func (e *HeldError) Error() string {
	if len(e.Conflicts) > 0 {
		return fmt.Sprintf("pipeline: the job is held: %d conflict(s) need a person (%d queued question(s))", len(e.Conflicts), len(e.Queue))
	}
	return fmt.Sprintf("pipeline: the job is held: %d queued question(s) await review", len(e.Queue))
}

// State is where the job is, in the vocabulary the service speaks (§6). A
// caller does not have to translate this error into a job state; there is only
// one state a held job can be in.
func (e *HeldError) State() alchemy.JobState { return alchemy.JobNeedsReview }
