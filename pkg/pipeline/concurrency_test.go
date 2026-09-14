package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// timedLLM records when calls overlap and which source each was for.
type timedLLM struct {
	hold time.Duration

	mu       sync.Mutex
	inFlight int
	peak     int
	open     map[string]int
	together bool
}

func (l *timedLLM) Name() string { return "fake-llm" }

func (l *timedLLM) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	// The chunk's text names its document, because the corpus below writes the
	// name into every chunk. It is the only thing in an LLMRequest that says
	// which source a call belongs to.
	source := ""
	if i := strings.Index(req.Prompt, "doc-"); i >= 0 && len(req.Prompt) >= i+6 {
		source = req.Prompt[i : i+6]
	}
	l.mu.Lock()
	l.inFlight++
	if l.inFlight > l.peak {
		l.peak = l.inFlight
	}
	if l.open == nil {
		l.open = map[string]int{}
	}
	l.open[source]++
	if len(l.open) > 1 {
		l.together = true
	}
	l.mu.Unlock()

	time.Sleep(l.hold)

	l.mu.Lock()
	l.inFlight--
	l.open[source]--
	if l.open[source] == 0 {
		delete(l.open, source)
	}
	l.mu.Unlock()
	return alchemy.LLMResponse{Text: `{"entities":[],"relations":[]}`}, nil
}

// manyDocuments builds a corpus of separate small documents, which is what a
// documentation folder is and what the serial path was slowest on.
func manyDocuments(t *testing.T, llm alchemy.LLM, n int) Request {
	t.Helper()
	sources := make([]Source, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("doc-%02d", i)
		sources = append(sources, doc(name+".md",
			fmt.Sprintf("# %s\n\n%s says the region is eu.\n", name, name)))
	}
	return Request{
		Sources:  sources,
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: llm},
	}
}

// A job that says how wide it may run, runs that wide across documents.
//
// The extractor has always taken a concurrency and the wire never carried one,
// so every job through the service ran at the built-in four whatever the
// caller's endpoint could take — and documents were read strictly one after
// another on top of that, though this file's own argument for keeping them
// apart is that they are independent. Sixty-seven documents of ordinary
// documentation took twenty-nine minutes that way, against fourteen seconds
// for one call of the same corpus to the same model.
func TestASetConcurrencyReadsDocumentsTogether(t *testing.T) {
	const docs = 6
	llm := &timedLLM{hold: 20 * time.Millisecond}
	req := manyDocuments(t, llm, docs)
	req.Concurrency = docs

	start := time.Now()
	if _, err := Run(context.Background(), req, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	llm.mu.Lock()
	peak, together := llm.peak, llm.together
	llm.mu.Unlock()
	if peak < 2 || !together {
		t.Errorf("peak %d calls in flight and documents overlapping = %v: the corpus was read one document at a time despite a concurrency of %d",
			peak, together, docs)
	}
	// Six documents held for 20ms each: serial is 120ms and together is one
	// hold plus overhead. The margin is wide because this asserts a shape, not
	// a speed — a loaded machine may be slow and must not be flaky.
	if elapsed > 100*time.Millisecond {
		t.Errorf("six documents took %s, which is the serial time; the width was not spent", elapsed)
	}
}

// A job that says nothing reads them one at a time, because §6's guarantee is
// about the document that has not started yet: a reviewer answering the first
// document's queue while the second is still to come. Start every document at
// once and there is no second document still to come — the guarantee does not
// weaken, it stops existing. What a caller does not ask for, they keep.
func TestAnUnsetConcurrencyStillReadsDocumentsOneAtATime(t *testing.T) {
	llm := &timedLLM{hold: 5 * time.Millisecond}
	if _, err := Run(context.Background(), manyDocuments(t, llm, 4), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	llm.mu.Lock()
	together := llm.together
	llm.mu.Unlock()
	if together {
		t.Error("two documents were read at once with no concurrency set, so a decision made mid-run has nothing left to reach")
	}
}
