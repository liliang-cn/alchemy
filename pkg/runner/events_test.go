package runner

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/pipeline"
)

// §7.2: the running count is what lets a job whose bill is growing faster than
// expected be cancelled while it runs. service.Event wants both halves — the
// total, and the breakdown for the operator who wants to know which stage is
// spending it.
func TestTranslateCarriesTheRunningCost(t *testing.T) {
	got := translate(pipeline.Event{
		Kind:  pipeline.EventProgress,
		Stage: "extract",
		ModelCalls: []alchemy.ModelCall{
			{Model: "gpt", Stage: "extract", Calls: 12, Tokens: 900},
			{Model: "emb", Stage: "embed", Calls: 3},
		},
		Counts: alchemy.Counts{Entities: 7},
	})
	if got.ModelCalls != 15 {
		t.Fatalf("ModelCalls = %d, want 15 (the running total across stages)", got.ModelCalls)
	}
	if len(got.ByStage) != 2 || got.ByStage[0].Model != "gpt" || got.ByStage[0].Calls != 12 {
		t.Fatalf("ByStage = %+v, want the pipeline's breakdown unchanged", got.ByStage)
	}
	if got.Stage != "extract" || got.Counts.Entities != 7 {
		t.Fatalf("event = %+v, want the stage and counts carried", got)
	}
	if got.At.IsZero() {
		t.Fatal("event has no timestamp")
	}
}

// §7.3: an operator watching a two-hour import learns about a conflict when it
// is found. The event carries it; the queue entry is not minted here — see
// translate for why.
func TestTranslateCarriesAConflictButNotAQueueItem(t *testing.T) {
	c := alchemy.Conflict{Kind: alchemy.ConflictEntityAttributes, Subject: "users"}
	got := translate(pipeline.Event{Kind: pipeline.EventConflict, Stage: "read", Conflict: &c})
	if got.Conflict == nil || got.Conflict.Subject != "users" {
		t.Fatalf("Conflict = %+v, want the one that was found", got.Conflict)
	}
	if got.Item != nil {
		t.Fatalf("Item = %+v; a queue entry minted before the graph is finished carries an index into a graph that is still growing", got.Item)
	}
}

// A watcher reading Message should be able to tell which of a hundred files
// the job has reached.
func TestTranslateNamesTheSource(t *testing.T) {
	got := translate(pipeline.Event{Kind: pipeline.EventStage, Stage: "read", Source: "manual.md"})
	if got.Message != "stage manual.md" {
		t.Fatalf("Message = %q, want it to name the source", got.Message)
	}
}
