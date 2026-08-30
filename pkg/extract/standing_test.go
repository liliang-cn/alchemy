package extract

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
)

// answers is a test's stand-in for the reviewer: what has been settled changes
// while the extraction runs, which is the whole of §6's first reason for gRPC.
type answers struct {
	mu       sync.Mutex
	told     []string
	named    string
	drop     string
	snapshot int
}

func (a *answers) settle(_ alchemy.Chunk, ents []alchemy.Entity, rels []alchemy.Relation) ([]alchemy.Entity, []alchemy.Relation) {
	a.mu.Lock()
	drop := a.drop
	a.mu.Unlock()
	if drop == "" {
		return ents, rels
	}
	kept := ents[:0:0]
	for _, e := range ents {
		if e.Type != drop {
			kept = append(kept, e)
		}
	}
	return kept, rels
}

func (a *answers) InForce() Settled {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.snapshot++
	return Settled{Told: append([]string(nil), a.told...), Named: a.named, Filter: a.settle}
}

func (a *answers) decide(told, named, drop string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.told, a.named, a.drop = append(a.told, told), named, drop
}

func (a *answers) snapshots() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshot
}

// §6: a standing answer is a thing the model is told and a thing the graph is
// held to, and §5b: whichever it is, the record has to be able to say so.
func TestAStandingAnswerIsToldToTheModelAndNamedInProvenance(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI"}],"relations":[]}`,
	}}
	a := &answers{}
	a.decide("Widget is not an entity type here.", "violation/unknown_entity_type/type=Widget", "")

	opts := testOptions(llm)
	opts.Standing = a.InForce
	got, err := Extract(context.Background(), testChunks("SuperAI is a cluster."), opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	sys := llm.requests()[0].System
	if !strings.Contains(sys, "Widget is not an entity type here.") {
		t.Errorf("the model was never told the standing answer:\n%s", sys)
	}
	if got.Entities[0].Provenance.RuleSet != "violation/unknown_entity_type/type=Widget" {
		t.Errorf("provenance rule set = %q, want the standing answer this chunk was extracted under",
			got.Entities[0].Provenance.RuleSet)
	}
}

// The prompt is a nudge and the filter is the guarantee. A model that ignores
// what it was told must not get its proposal into the graph anyway.
func TestAProposalAStandingAnswerSettlesDoesNotEnterTheGraph(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Widget","name":"W1"},{"type":"Cluster","name":"SuperAI"}],"relations":[]}`,
	}}
	a := &answers{}
	a.decide("Widget is not an entity type here.", "violation/unknown_entity_type/type=Widget", "Widget")

	opts := testOptions(llm)
	opts.Standing = a.InForce
	got, err := Extract(context.Background(), testChunks("SuperAI is a cluster, W1 is a widget."), opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].Name != "SuperAI" {
		t.Fatalf("entities = %+v, want only the one no standing answer settles", got.Entities)
	}
	// The call was made and is still billed. Filtering costs a model call that
	// was already paid for; pretending otherwise would understate the bill
	// §7.2 promises is honest.
	if got.ModelCalls[0].Calls != 1 {
		t.Errorf("calls = %d, want the one this chunk actually bought", got.ModelCalls[0].Calls)
	}
}

// §6 again, and the reason a slice would not do: the answers are asked for per
// chunk, so one made after chunk 0 reaches chunk 1.
func TestTheStandingAnswersAreAskedForOncePerChunk(t *testing.T) {
	a := &answers{}
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"A"}],"relations":[]}`,
		1: `{"entities":[{"type":"Cluster","name":"B"}],"relations":[]}`,
		2: `{"entities":[{"type":"Cluster","name":"C"}],"relations":[]}`,
	}}
	opts := testOptions(llm)
	opts.Standing = a.InForce
	if _, err := Extract(context.Background(), testChunks("a", "b", "c"), opts); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := a.snapshots(); got != 3 {
		t.Fatalf("the answers were snapshotted %d times for 3 chunks; a decision made between two of them would reach neither", got)
	}
}

// §8.2's cache stores what the model said, not what a reviewer has since
// decided. A hit that skipped the standing answers would let a rule stop
// applying the moment the same paragraph is seen twice.
func TestACachedAnswerIsStillPutThroughTheStandingAnswers(t *testing.T) {
	chunks := testChunks("SuperAI is a cluster, W1 is a widget.")
	reply := `{"entities":[{"type":"Widget","name":"W1"},{"type":"Cluster","name":"SuperAI"}],"relations":[]}`

	store := cache.NewMemory(16)
	warm := testOptions(&fakeLLM{replies: map[int]string{0: reply}})
	warm.Cache = store
	if _, err := Extract(context.Background(), chunks, warm); err != nil {
		t.Fatalf("Extract(warm): %v", err)
	}

	a := &answers{}
	a.decide("", "violation/unknown_entity_type/type=Widget", "Widget")
	hit := testOptions(&fakeLLM{replies: map[int]string{0: reply}})
	hit.Cache = store
	hit.Standing = a.InForce
	got, err := Extract(context.Background(), chunks, hit)
	if err != nil {
		t.Fatalf("Extract(hit): %v", err)
	}
	for _, e := range got.Entities {
		if e.Type == "Widget" {
			t.Fatalf("the cache served a proposal a standing answer settles: %+v", got.Entities)
		}
	}
	if got.Entities[0].Provenance.RuleSet == "" {
		t.Error("a cache hit lost the fact that this chunk was extracted under a standing answer")
	}
}

// The determinism the package had must survive: a run nobody decided anything
// during is the run every existing caller makes, and it is still a pure
// function of its input. See TestOutputIsIdenticalAtEveryConcurrency for the
// same claim without a Standing at all.
func TestOutputIsIdenticalAtEveryConcurrencyWithAFixedStandingAnswer(t *testing.T) {
	var first []byte
	for _, n := range []int{1, 2, 3, 8, 24, 64} {
		llm, chunks := busyCorpus()
		a := &answers{}
		a.decide("Person is not wanted here.", "violation/unknown_entity_type/type=Person", "Person")
		opts := testOptions(llm)
		opts.Concurrency = n
		opts.Standing = a.InForce
		got, err := Extract(context.Background(), chunks, opts)
		if err != nil {
			t.Fatalf("Extract at concurrency %d: %v", n, err)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
			continue
		}
		if string(b) != string(first) {
			t.Errorf("concurrency %d produced a different result:\n%s\nwant\n%s", n, b, first)
		}
	}
}
