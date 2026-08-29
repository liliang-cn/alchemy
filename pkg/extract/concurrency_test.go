package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// busyCorpus is deliberately awkward: entities that recur across chunks under
// different spellings, relations whose ends are introduced in other chunks,
// chunks that are honestly empty, a call that fails and a reply that cannot be
// read. Anything order-dependent in the merge shows up here or nowhere.
func busyCorpus() (*fakeLLM, []alchemy.Chunk) {
	replies := map[int]string{}
	errs := map[int]error{}
	for i := 0; i < 24; i++ {
		switch i % 6 {
		case 0:
			replies[i] = fmt.Sprintf(`{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"a%d":"v"},"confidence":0.5},{"type":"Node","name":"node-%d"}],
			  "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-%d","to_type":"Node"}]}`, i, i, i)
		case 1:
			replies[i] = `{"entities":[{"type":"Cluster","name":"superai"}],"relations":[{"type":"MENTIONS","from":"SuperAI","to":"node-0"}]}`
		case 2:
			replies[i] = `{"entities":[],"relations":[]}`
		case 3:
			replies[i] = "Here is the JSON:\n```json\n" + fmt.Sprintf(`{"entities":[{"type":"Person","name":"Ada %d"}],"relations":[]}`, i) + "\n```"
		case 4:
			errs[i] = errors.New("429 rate limited")
		case 5:
			replies[i] = "I'm sorry, I can't help with that."
		}
	}
	texts := make([]string, 24)
	for i := range texts {
		texts[i] = fmt.Sprintf("chunk body %d", i)
	}
	return &fakeLLM{name: "m", tokens: 7, replies: replies, errs: errs}, testChunks(texts...)
}

// A graph that changes shape with a tuning knob is not reproducible, and no
// downstream stage could tell you which of the two shapes it got.
func TestOutputIsIdenticalAtEveryConcurrency(t *testing.T) {
	var first []byte
	for _, n := range []int{1, 2, 3, 8, 24, 64} {
		llm, chunks := busyCorpus()
		got, err := Extract(context.Background(), chunks, Options{
			LLM: llm, Vocabulary: testVocab(), OntologyID: "sds@3", Concurrency: n,
		})
		if err != nil {
			t.Fatalf("Extract at concurrency %d: %v", n, err)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
			if len(got.Entities) == 0 || len(got.Relations) == 0 || len(got.Unread) == 0 {
				t.Fatalf("the corpus is not exercising anything: %#v", got)
			}
			continue
		}
		if string(b) != string(first) {
			t.Errorf("concurrency %d produced a different result:\n%s\nwant\n%s", n, b, first)
		}
	}
}

// countingLLM wraps a fake and watches how many calls are in flight at once.
// It blocks until `barrier` of them are, so an extractor that is secretly
// sequential fails by timing out rather than by being subtly slow.
type countingLLM struct {
	inner   *fakeLLM
	barrier int

	mu       sync.Mutex
	inFlight int
	peak     int
	reached  chan struct{}
	once     sync.Once
}

func newCountingLLM(inner *fakeLLM, barrier int) *countingLLM {
	return &countingLLM{inner: inner, barrier: barrier, reached: make(chan struct{})}
}

func (c *countingLLM) Name() string { return c.inner.Name() }

func (c *countingLLM) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.peak {
		c.peak = c.inFlight
	}
	atBarrier := c.inFlight >= c.barrier
	c.mu.Unlock()
	if atBarrier {
		c.once.Do(func() { close(c.reached) })
	}
	select {
	case <-c.reached:
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return c.inner.Complete(ctx, req)
}

func (c *countingLLM) peakInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

// §8.2: the bound exists because the caller's endpoint has a rate limit. It has
// to be a real bound — a limit of 3 that lets 16 calls go out is a limit that
// will be discovered as a 429 storm — and it has to be a real parallelism.
func TestConcurrencyIsABoundAndIsActuallyParallel(t *testing.T) {
	llm, chunks := busyCorpus()
	c := newCountingLLM(llm, 3)
	if _, err := Extract(context.Background(), chunks, Options{
		LLM: c, Vocabulary: testVocab(), OntologyID: "sds@3", Concurrency: 3,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if peak := c.peakInFlight(); peak != 3 {
		t.Errorf("peak calls in flight = %d, want exactly the 3 that were allowed", peak)
	}
}

// The default is neither one at a time nor everything at once.
func TestDefaultConcurrencyIsBoundedAndParallel(t *testing.T) {
	llm, chunks := busyCorpus()
	c := newCountingLLM(llm, 2)
	if _, err := Extract(context.Background(), chunks, testOptions(c)); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	peak := c.peakInFlight()
	if peak < 2 {
		t.Errorf("peak in flight = %d: the default extracts one chunk at a time", peak)
	}
	if peak >= len(chunks) {
		t.Errorf("peak in flight = %d of %d chunks: the default is no bound at all", peak, len(chunks))
	}
}

// cancelAfterFirst cancels the run once one call has been answered, which is
// the case that matters: a job cancelled from WatchJob because its bill was
// growing faster than expected (§7.2).
type cancelAfterFirst struct {
	inner  *fakeLLM
	cancel context.CancelFunc

	mu sync.Mutex
	n  int
}

func (c *cancelAfterFirst) Name() string { return c.inner.Name() }

func (c *cancelAfterFirst) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	resp, err := c.inner.Complete(ctx, req)
	c.mu.Lock()
	c.n++
	first := c.n == 1
	c.mu.Unlock()
	if first {
		c.cancel()
	}
	return resp, err
}

// A cancelled run stops buying, and says it was cancelled. Returning the chunks
// that happened to finish as though they were the corpus is the partial result
// presented as a whole one that this package exists to prevent.
func TestCancellationStopsTheRunAndIsReported(t *testing.T) {
	llm, chunks := busyCorpus()
	ctx, cancel := context.WithCancel(context.Background())
	c := &cancelAfterFirst{inner: llm, cancel: cancel}
	got, err := Extract(ctx, chunks, Options{
		LLM: c, Vocabulary: testVocab(), OntologyID: "sds@3", Concurrency: 1,
	})
	if err == nil {
		t.Fatal("want an error from a cancelled run, got a result that looks complete")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("the error does not name the cancellation: %v", err)
	}
	if len(got.ModelCalls) == 0 {
		t.Fatal("a cancelled run still spent something and must still report it")
	}
	// It stopped rather than working through the rest of the corpus.
	if n := got.ModelCalls[0].Calls; n == 0 || n >= len(chunks) {
		t.Errorf("Calls = %d of %d chunks: a cancelled run must stop buying", n, len(chunks))
	}
}
