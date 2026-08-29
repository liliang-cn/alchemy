package embed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// fakeEmbedder is a deterministic stand-in for a caller's embedding endpoint.
//
// The vector it returns is a function of the text alone, because that is the
// only thing a real embedder sees: it is handed a batch of strings and has no
// idea which chunk any of them came from. That is precisely what makes a
// misaligned batch invisible to the provider and visible only here — a test
// that computed the expectation from the chunk index instead would agree with
// an off-by-one as readily as with the truth.
type fakeEmbedder struct {
	name string
	// errFor fails any batch containing this text, standing in for an endpoint
	// that rejects one request and answers the next.
	errFor map[string]error
	// shortFor returns one vector fewer than asked for, for a batch containing
	// this text: the provider bug that silently misaligns everything after it.
	shortFor map[string]bool
	// longFor returns one vector more than asked for, for a batch containing
	// this text: the same class of provider bug, seen from the other side.
	longFor map[string]bool
	// emptyFor returns a zero-dimension vector for this text.
	emptyFor map[string]bool
	// widthFor returns a vector of this many dimensions for this text, standing
	// in for a provider that changed model mid-run.
	widthFor map[string]int
	// before runs before a batch is answered, so a test can cancel mid-run or
	// watch how many calls are in flight.
	before func(texts []string)

	mu      sync.Mutex
	batches [][]string
}

func (f *fakeEmbedder) Name() string {
	if f.name == "" {
		return "fake-embedder"
	}
	return f.name
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if f.before != nil {
		f.before(texts)
	}
	f.mu.Lock()
	f.batches = append(f.batches, append([]string(nil), texts...))
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, t := range texts {
		if err := f.errFor[t]; err != nil {
			return nil, err
		}
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		if f.emptyFor[t] {
			out = append(out, []float32{})
			continue
		}
		if w, ok := f.widthFor[t]; ok {
			out = append(out, make([]float32, w))
			continue
		}
		out = append(out, wantVector(t))
	}
	for _, t := range texts {
		if f.shortFor[t] && len(out) > 0 {
			out = out[:len(out)-1]
			break
		}
		if f.longFor[t] {
			out = append(out, wantVector("an extra vector nobody asked for"))
			break
		}
	}
	return out, nil
}

// sentBatches is what the endpoint was actually handed. Tests use it to assert
// what did *not* reach the model (an empty chunk costs a call and buys a vector
// describing nothing) and how the work was cut up — never as a call count
// standing in for a correctness check.
func (f *fakeEmbedder) sentBatches() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.batches...)
}

func (f *fakeEmbedder) sentTexts() []string {
	var out []string
	for _, b := range f.sentBatches() {
		out = append(out, b...)
	}
	return out
}

// wantVector is the vector a text must map to. FNV-1a over the bytes, spread
// across three dimensions, so no two test texts collide and a vector attached
// to the wrong chunk cannot look right by accident.
func wantVector(text string) []float32 {
	const (
		offset = 2166136261
		prime  = 16777619
	)
	out := make([]float32, 3)
	for d := range out {
		h := uint32(offset + d)
		for i := 0; i < len(text); i++ {
			h ^= uint32(text[i])
			h *= prime
		}
		out[d] = float32(h % 100000)
	}
	return out
}

// testChunks builds a corpus whose texts are all distinct, with byte offsets,
// the way the chunker hands them over.
func testChunks(texts ...string) []alchemy.Chunk {
	out := make([]alchemy.Chunk, len(texts))
	at := 0
	for i, t := range texts {
		out[i] = alchemy.Chunk{
			Index:    i,
			Text:     t,
			Source:   "architecture.md",
			Strategy: "heading",
			Start:    at,
			End:      at + len(t),
		}
		at += len(t)
	}
	return out
}

// bodies is n distinct chunk texts.
func bodies(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("chunk body %d about %s", i, strings.Repeat("x", i%5))
	}
	return out
}

var errEndpointDown = errors.New("503 endpoint down")

// usageEmbedder is an endpoint that reports what a call cost. The port
// alchemy.Embedder has nowhere to say it — Embed returns vectors and an error
// and nothing else — so a provider that knows its token count says so through
// the optional interface this package offers instead of the shared contract
// three other stages speak.
type usageEmbedder struct {
	*fakeEmbedder
	// perText is the token cost the provider claims per text embedded.
	perText int
}

func (u *usageEmbedder) EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) {
	vecs, err := u.fakeEmbedder.Embed(ctx, texts)
	// Tokens are reported even when the call failed: a rejected request that
	// was still billed is exactly the case a cost report must not lose.
	return vecs, u.perText * len(texts), err
}
