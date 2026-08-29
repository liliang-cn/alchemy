package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// duplicateTable declares one table twice with different columns, which
// pkg/source/ddl reports as a conflict: nothing in the file says which
// declaration is right. It is found while the first source is being read,
// which is what makes it the right subject for the "minute three" test.
const duplicateTable = `
CREATE TABLE customers (id INT PRIMARY KEY, name TEXT);
CREATE TABLE customers (id INT PRIMARY KEY, email TEXT);
`

func conflictingJob(t *testing.T) Request {
	t.Helper()
	req := regionRequest(t,
		Source{Name: "schema.sql", Kind: alchemy.SourceDDL, Open: openString(duplicateTable)},
		doc("eu.md", docEU))
	req.Part = ontology.PartProse
	return req
}

// collect drains events until the channel is closed and returns them in order.
func collect(t *testing.T, events <-chan Event) []Event {
	t.Helper()
	var got []Event
	for ev := range events {
		got = append(got, ev)
	}
	return got
}

// §7.3: "WatchJob emits a conflict when it is found, not at the end. An
// operator watching a two-hour import should learn in minute three that it
// will need them, not at minute one hundred and twenty."
//
// The assertion is therefore about position, not presence: the conflict is
// reported while the job is still doing the work the operator would otherwise
// have waited through.
func TestAConflictIsReportedWhenItIsFoundAndNotAtTheEnd(t *testing.T) {
	events := make(chan Event, 64)
	done := make(chan []Event, 1)
	go func() { done <- collect(t, events) }()

	if _, err := Run(context.Background(), conflictingJob(t), events); err == nil {
		t.Fatal("Run: want a hold, got none")
	}
	got := <-done

	conflict, verify := -1, -1
	for i, ev := range got {
		if ev.Kind == EventConflict && conflict < 0 {
			conflict = i
			if ev.Conflict == nil {
				t.Error("a conflict event carries no conflict")
			}
		}
		if ev.Kind == EventStage && ev.Stage == stageVerify {
			verify = i
		}
	}
	if conflict < 0 {
		t.Fatalf("no conflict was ever reported: %+v", got)
	}
	if verify < 0 {
		t.Fatalf("the job never reported reaching the verify stage: %+v", got)
	}
	if conflict > verify {
		t.Errorf("the conflict was reported at %d, after verification at %d; it was found while reading", conflict, verify)
	}
}

// The running counts are what §7.2 streams so that "a job whose bill is
// growing faster than expected can be cancelled while it runs rather than
// after it finishes". A total that only arrives at the end is a bill.
func TestProgressCarriesTheRunningCountsAndCost(t *testing.T) {
	events := make(chan Event, 256)
	done := make(chan []Event, 1)
	go func() { done <- collect(t, events) }()

	if _, err := Run(context.Background(), mixedJob(t), events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := <-done

	var spent bool
	for _, ev := range got {
		if ev.Kind != EventProgress {
			continue
		}
		for _, c := range ev.ModelCalls {
			if c.Calls > 0 {
				spent = true
			}
		}
	}
	if !spent {
		t.Errorf("no progress event reported what the job had spent: %+v", got)
	}
}

// A caller that wants no progress passes no channel, and that is not a
// special case anybody should have to remember.
func TestANilEventChannelIsSafe(t *testing.T) {
	if _, err := Run(context.Background(), mixedJob(t), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A consumer that stops reading must not be able to stop the job. The events
// channel is advisory — everything on it is in the result too — so the job
// outranks it, and a caller who wandered off gets a finished job rather than a
// deadlocked one.
func TestASlowConsumerCannotStallTheJob(t *testing.T) {
	events := make(chan Event) // unbuffered, and nobody is reading
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), mixedJob(t), events)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not finish: a consumer that never reads deadlocked the job")
	}
}

// The other half of that decision, and the one §7.3 actually promises: a
// conflict reaches the operator *while the job is still running*.
//
// The test makes that timing structural rather than hopeful. The extraction
// blocks until the consumer has seen the conflict, so a pipeline that queued
// conflicts until the end, or dropped them when the consumer fell behind,
// would deadlock here instead of quietly failing an assertion about ordering.
func TestAConflictReachesTheOperatorWhileTheJobIsStillRunning(t *testing.T) {
	events := make(chan Event) // unbuffered: nothing arrives early by luck
	seen := make(chan struct{})
	req := conflictingJob(t)
	llm := twoRegionsLLM()
	// Minute three, in the small: the model does not answer until the operator
	// has been told, which is the promise stated as a happens-before.
	llm.hook = func() {
		select {
		case <-seen:
		case <-time.After(10 * time.Second):
			t.Error("the extraction ran to completion without the conflict having been reported")
		}
	}
	req.Models.LLM = llm

	go func() {
		var closed bool
		for ev := range events {
			// Slow on purpose: the consumer is behind the job throughout.
			time.Sleep(time.Millisecond)
			if ev.Kind == EventConflict && !closed {
				closed = true
				close(seen)
			}
		}
	}()

	_, err := Run(context.Background(), req, events)
	var held *HeldError
	if !asHeld(err, &held) {
		t.Fatalf("Run = %v, want a hold", err)
	}
	select {
	case <-seen:
	default:
		t.Error("the job finished without the conflict having reached the consumer")
	}
}
