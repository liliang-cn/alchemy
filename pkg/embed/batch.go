package embed

import (
	"context"
	"fmt"
	"sync"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// DefaultBatchSize is how many chunks go in one call when the caller names no
// size.
//
// The Embedder port hands the implementation "everything it wants embedded at
// once", so the size of a call is this package's decision. Thirty-two, for two
// reasons that pull in opposite directions and meet here.
//
// Upward: a call has a round trip whatever it carries, and one chunk per call
// against a remote endpoint spends the whole wall-clock waiting. Downward:
// every real embedding endpoint caps a request, and it caps it two ways — by
// number of inputs (the smallest published caps are around 96 to 128) and by
// tokens in the request. The chunker's default budget is 1000 tokens per chunk
// (chunk.DefaultMaxTokens), so 32 chunks is roughly 32k tokens in one request:
// comfortably inside the smallest allowances, and comfortably under every input
// cap we know of.
//
// The asymmetry decides the tie. Too small costs wall-clock, which is visible
// and adjustable. Too large costs a rejected request on the caller's first real
// corpus, and a batch rejected for size is a batch whose chunks all land in
// Unread — the whole batch is the blast radius, which is exactly the argument
// against being greedy here.
const DefaultBatchSize = 32

// batchesOf cuts the chunks into calls, in order. The last one is short
// whenever the count does not divide evenly, which is the common case and the
// one where alignment bugs live.
func batchesOf(chunks []alchemy.Chunk, size int) [][]alchemy.Chunk {
	if size <= 0 {
		size = DefaultBatchSize
	}
	var out [][]alchemy.Chunk
	for start := 0; start < len(chunks); start += size {
		end := start + size
		if end > len(chunks) {
			end = len(chunks)
		}
		out = append(out, chunks[start:end])
	}
	return out
}

// UsageEmbedder is an alchemy.Embedder that also reports what a call cost.
//
// The shared port cannot: Embed returns vectors and an error and nothing else,
// while §7.2 promises a job reports what it spent, by model and stage. Widening
// alchemy.Embedder would change a contract the chunker and the service also
// speak, for the sake of one stage, so the optional interface lives here. An
// embedder that implements it has its tokens counted; one that does not is
// reported with Calls and Tokens 0, which is exactly what alchemy.ModelCall
// documents 0 to mean — "the provider does not report usage", never "this was
// free".
type UsageEmbedder interface {
	alchemy.Embedder
	// EmbedUsage embeds the texts and reports the tokens the call cost. It must
	// answer the same way Embed does: one vector per text, in order.
	EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error)
}

// call makes one request, through UsageEmbedder when the provider offers it.
func call(ctx context.Context, e alchemy.Embedder, texts []string) ([][]float32, int, error) {
	if u, ok := e.(UsageEmbedder); ok {
		return u.EmbedUsage(ctx, texts)
	}
	vecs, err := e.Embed(ctx, texts)
	return vecs, 0, err
}

// batchOutcome is what happened to one call. It is a value rather than a write
// into a shared result on purpose: the calls finish in whatever order the
// endpoint answers, and the assembly puts them back in chunk order, which is
// what makes the output independent of Concurrency (§8.2).
type batchOutcome struct {
	chunks []alchemy.Chunk
	// vectors is one per chunk, in the same order, and is only read when both
	// fatal is nil and failed is empty.
	vectors [][]float32
	// failed is why this batch produced no vectors, when it produced none. The
	// chunks then go to Unread rather than quietly having no vector.
	failed string
	// fatal is a broken provider rather than a failed call: a batch answered
	// with the wrong number of vectors. It stops the run, because the contract
	// this package rests on — the n-th vector embeds the n-th text — is the one
	// the provider just broke, and nothing it said in any other batch is worth
	// more than a guess after that.
	fatal error
	// calls counts the call whether or not it succeeded: a call that failed was
	// still paid for, and a cost report that counts only successes understates
	// the bill (§7.2).
	calls int
	// tokens is what the provider reported, when it reports anything.
	tokens int
}

// embedBatch makes one call.
func embedBatch(ctx context.Context, batch []alchemy.Chunk, opts Options) batchOutcome {
	out := batchOutcome{chunks: batch, calls: 1}
	texts := make([]string, len(batch))
	for i, c := range batch {
		texts[i] = c.Text
	}

	vecs, tokens, err := call(ctx, opts.Embedder, texts)
	// Counted before the error is looked at: a request that was rejected after
	// the provider read it was still billed, and a cost report that counts only
	// what succeeded understates itself exactly when the caller is most likely
	// to be reading it (§7.2).
	out.tokens = tokens
	if err != nil {
		out.failed = err.Error()
		return out
	}
	// The count check is the whole reason this package owns batching. A
	// provider that returns a different number of vectors than it was given
	// texts has misaligned everything after the missing one, and every vector
	// past that point is well-formed, plausible, and about the wrong chunk.
	// Both numbers are in the message because "the embedder misbehaved" sends a
	// reader to our code and "asked for 32, got 31" sends them to their
	// provider, which is where the bug is.
	if len(vecs) != len(batch) {
		out.fatal = fmt.Errorf("embed: %s returned %d vectors for a batch of %d texts (chunks %d-%d); "+
			"the n-th vector must embed the n-th text, so nothing here can be aligned",
			opts.Embedder.Name(), len(vecs), len(batch), batch[0].Index, batch[len(batch)-1].Index)
		return out
	}
	out.vectors = vecs
	return out
}

// defaultConcurrency is the number of batches in flight when the caller names
// none.
//
// It is not derived from the CPU count, and that is the whole point of §8.2:
// the work is a network call to the caller's endpoint, and the thing that
// breaks first is that endpoint's rate limit, not this process. A default tuned
// to our cores would scale itself straight into someone else's 429s. Four
// matches pkg/extract for the same reason it chose four — too low costs
// wall-clock, which is visible and adjustable; too high costs a rate limit on
// the caller's first run, which looks like the service being broken.
const defaultConcurrency = 4

// run makes every call, at most Concurrency at a time.
//
// Each worker writes to its own slot and nothing is merged here. That is what
// makes the result independent of Concurrency: the workers decide when a reply
// arrives, assemble decides where it goes, and the two decisions never meet.
func run(ctx context.Context, batches [][]alchemy.Chunk, opts Options) []batchOutcome {
	out := make([]batchOutcome, len(batches))

	n := opts.Concurrency
	if n <= 0 {
		n = defaultConcurrency
	}
	if n > len(batches) {
		n = len(batches)
	}

	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i, b := range batches {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, b []alchemy.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()
			// Checked here rather than left to the endpoint: a call this
			// process never made was never paid for, and counting it would
			// overstate the one number §7.2 promises is honest. It is also what
			// makes a cancel prompt — the batches still queued behind the
			// semaphore fall straight through instead of buying a round trip
			// each on the way out.
			if err := ctx.Err(); err != nil {
				out[i] = batchOutcome{chunks: b, failed: err.Error()}
				return
			}
			out[i] = embedBatch(ctx, b, opts)
		}(i, b)
	}
	wg.Wait()
	return out
}
