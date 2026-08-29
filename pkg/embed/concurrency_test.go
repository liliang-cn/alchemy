package embed

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// busyCorpus is deliberately awkward: enough chunks for several batches, a
// batch size that divides none of it evenly, a batch that fails in the middle
// and one that fails at the end. Anything order-dependent shows up here or
// nowhere.
func busyCorpus() (*fakeEmbedder, []alchemy.Chunk) {
	chunks := testChunks(bodies(37)...)
	return &fakeEmbedder{
		name: "text-embedding-fake",
		errFor: map[string]error{
			chunks[13].Text: errEndpointDown,
			chunks[36].Text: errEndpointDown,
		},
	}, chunks
}

// A result that changes shape with a tuning knob is not reproducible, and no
// reader of the vectors could tell you which of the two shapes they got.
// Concurrency decides when a batch is answered; it must not decide where the
// answer lands.
func TestOutputIsIdenticalAtEveryConcurrency(t *testing.T) {
	var first []byte
	for _, n := range []int{1, 2, 3, 5, 16, 64} {
		emb, chunks := busyCorpus()
		got, err := Embed(context.Background(), chunks, Options{
			Embedder: emb, BatchSize: 6, Concurrency: n,
		})
		if err != nil {
			t.Fatalf("Embed at concurrency %d: %v", n, err)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
			if len(got.Vectors) == 0 || len(got.Unread) == 0 {
				t.Fatalf("the corpus is not exercising anything: %d vectors, %d unread",
					len(got.Vectors), len(got.Unread))
			}
			continue
		}
		if string(b) != string(first) {
			t.Errorf("concurrency %d produced a different result:\n%s\nwant\n%s", n, b, first)
		}
	}
}

// And the vectors are in chunk order at every level, not merely in the same
// order each time — a stable wrong order would pass the test above.
func TestVectorsComeBackInChunkOrder(t *testing.T) {
	for _, n := range []int{1, 4, 64} {
		emb, chunks := busyCorpus()
		got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 6, Concurrency: n})
		if err != nil {
			t.Fatalf("Embed at concurrency %d: %v", n, err)
		}
		prev := -1
		for _, v := range got.Vectors {
			if v.Chunk <= prev {
				t.Fatalf("concurrency %d: chunk %d follows chunk %d; vectors are not in chunk order", n, v.Chunk, prev)
			}
			prev = v.Chunk
			if !equalVec(v.Values, wantVector(chunks[v.Chunk].Text)) {
				t.Errorf("concurrency %d: vector on chunk %d does not embed that chunk's text", n, v.Chunk)
			}
		}
	}
}

// barrier blocks every call until `want` of them are in flight, so an
// implementation that is secretly sequential fails by timing out rather than by
// being quietly slower than it claims.
type barrier struct {
	want int

	mu       sync.Mutex
	inFlight int
	peak     int
	timedOut bool

	once sync.Once
	open chan struct{}
}

func newBarrier(want int) *barrier { return &barrier{want: want, open: make(chan struct{})} }

func (b *barrier) enter([]string) {
	b.mu.Lock()
	b.inFlight++
	if b.inFlight > b.peak {
		b.peak = b.inFlight
	}
	reached := b.inFlight >= b.want
	b.mu.Unlock()

	if reached {
		b.once.Do(func() { close(b.open) })
	}
	select {
	case <-b.open:
	case <-time.After(2 * time.Second):
		b.mu.Lock()
		b.timedOut = true
		b.mu.Unlock()
	}
	b.mu.Lock()
	b.inFlight--
	b.mu.Unlock()
}

// The work here is a network call to the caller's endpoint (§8.2), so batches
// have to overlap or a large corpus is read one round trip at a time.
func TestBatchesRunConcurrently(t *testing.T) {
	chunks := testChunks(bodies(24)...)
	b := newBarrier(4)
	emb := &fakeEmbedder{before: b.enter}

	if _, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4, Concurrency: 4}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timedOut {
		t.Fatalf("no 4 batches were ever in flight at once (peak %d): the calls are sequential", b.peak)
	}
}

// And the bound is a bound. §8.2: the endpoint's rate limit is what breaks
// first, so a Concurrency of 4 that runs 12 calls at once is the setting doing
// the opposite of what it is for.
func TestConcurrencyIsAnUpperBound(t *testing.T) {
	chunks := testChunks(bodies(40)...)
	var mu sync.Mutex
	inFlight, peak := 0, 0
	emb := &fakeEmbedder{before: func([]string) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
	}}

	if _, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 2, Concurrency: 3}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > 3 {
		t.Errorf("%d calls were in flight at once, want at most the Concurrency of 3", peak)
	}
}
