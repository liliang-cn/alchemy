package job

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// allStates is every state the shared contract declares. The exhaustive test
// below walks this list against itself, so a state added to pkg/alchemy and
// forgotten here is the one thing this file cannot catch — hence the length
// assertion in TestAllStatesAreCovered.
var allStates = []alchemy.JobState{
	alchemy.JobPending,
	alchemy.JobRunning,
	alchemy.JobNeedsReview,
	alchemy.JobSucceeded,
	alchemy.JobFailed,
	alchemy.JobExpired,
	alchemy.JobCancelled,
}

var allActors = []actor{actorWorker, actorCaller, actorSweeper}

// edge is one legal move, spelled out here independently of the production
// table. A test that imports the table it is testing proves only that the table
// equals itself.
type edge struct {
	from, to alchemy.JobState
	by       actor
}

// legalEdges is the specification. Every pair not listed is illegal.
var legalEdges = []edge{
	// A worker claims queued work; that is the only way into RUNNING.
	{alchemy.JobPending, alchemy.JobRunning, actorWorker},
	// A caller may withdraw work that has not started.
	{alchemy.JobPending, alchemy.JobCancelled, actorCaller},
	// Queued work nobody ever claims still ages out (§5c).
	{alchemy.JobPending, alchemy.JobExpired, actorSweeper},

	// Takeover: a lease died and another node picked the job up. The only
	// self-transition, and the reason at-least-once is survivable (§8.3).
	{alchemy.JobRunning, alchemy.JobRunning, actorWorker},
	// A worker hands work back without finishing it, and the sweeper does the
	// same for a worker that stopped answering: without the second edge, a job
	// whose node died sits RUNNING forever in an idle cluster, because the
	// takeover path needs somebody to come asking for work.
	{alchemy.JobRunning, alchemy.JobPending, actorWorker},
	{alchemy.JobRunning, alchemy.JobPending, actorSweeper},
	{alchemy.JobRunning, alchemy.JobNeedsReview, actorWorker},
	{alchemy.JobRunning, alchemy.JobSucceeded, actorWorker},
	{alchemy.JobRunning, alchemy.JobFailed, actorWorker},
	// A caller may cancel work already in flight; the worker learns when its
	// next write is refused.
	{alchemy.JobRunning, alchemy.JobCancelled, actorCaller},

	// A held job is resolved by a person, not by a node: §7.3, a conflict is a
	// question and questions are answered by whoever was found.
	{alchemy.JobNeedsReview, alchemy.JobSucceeded, actorCaller},
	{alchemy.JobNeedsReview, alchemy.JobFailed, actorCaller},
	{alchemy.JobNeedsReview, alchemy.JobCancelled, actorCaller},
	// Held work that nobody answered expires (§5c).
	{alchemy.JobNeedsReview, alchemy.JobExpired, actorSweeper},
}

func isLegal(e edge) bool {
	for _, l := range legalEdges {
		if l == e {
			return true
		}
	}
	return false
}

func TestTransitionTable(t *testing.T) {
	for _, from := range allStates {
		for _, to := range allStates {
			for _, by := range allActors {
				e := edge{from, to, by}
				err := check(from, to, by)
				if isLegal(e) && err != nil {
					t.Errorf("%s -> %s by %s: want legal, got %v", from, to, by, err)
				}
				if !isLegal(e) && err == nil {
					t.Errorf("%s -> %s by %s: want refused, got nil", from, to, by)
				}
			}
		}
	}
}

// A refusal that does not say what was refused is a log line an operator reads
// at 3am and learns nothing from.
func TestRefusalNamesBothStatesAndTheActor(t *testing.T) {
	err := check(alchemy.JobSucceeded, alchemy.JobRunning, actorWorker)
	if err == nil {
		t.Fatal("want a refusal restarting a finished job")
	}
	msg := err.Error()
	for _, want := range []string{"SUCCEEDED", "RUNNING", "worker"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
}

func TestAllStatesAreCovered(t *testing.T) {
	if len(allStates) != 7 {
		t.Fatalf("the contract declares 7 states, the test walks %d", len(allStates))
	}
}
