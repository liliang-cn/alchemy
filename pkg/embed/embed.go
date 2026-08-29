// Package embed turns the chunks that survived review into vectors.
//
// It is the last stage of the pipeline (DESIGN.md §3) and it is last for a
// reason §5c states plainly: vectors are not reviewable — nobody can eyeball a
// 768-dimensional vector — so they are computed for whatever text came out of
// review rather than for whatever went in. Embedding rejected content wastes
// the call, and embedding before edits leaves vectors describing text that has
// since changed.
//
// Two obligations shape this package more than anything else does:
//
//   - A vector belongs to exactly one chunk, and says which. The Embedder port
//     takes a slice and returns a slice, so alignment is this package's
//     business and nobody else's; a vector attached to the wrong chunk is not
//     an error anyone sees, it is a search result that is quietly wrong forever.
//   - A batch that failed is never a chunk that has no vector for no stated
//     reason. It goes to Unread, the way §5 requires source material that could
//     not be read to be named rather than silently omitted.
package embed

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// stage is what this package reports as alchemy.ModelCall.Stage (§7.2).
const stage = "embed"

// Options is what one embedding run was given.
type Options struct {
	// Embedder is the caller's model (§6). Nil is legal and means no vectors
	// were asked for; see Embed.
	Embedder alchemy.Embedder
	// BatchSize is how many chunks go in one call. Zero means DefaultBatchSize.
	BatchSize int
	// Concurrency bounds the batches in flight. Zero means the default.
	Concurrency int
}

// Result is what one embedding run produced.
type Result struct {
	// Vectors are in chunk order, whatever order the endpoint answered in.
	Vectors []alchemy.Vector
	// Unread names the chunks that came back with no usable vector, with why —
	// a failed call, a cancel, or a vector with no dimensions. Never empty for
	// a chunk that has no vector: §5's rule that unread source material is
	// named rather than silently omitted is the same rule here.
	Unread []alchemy.Unread
	// ModelCalls is what the run spent, by model and stage (§7.2).
	ModelCalls []alchemy.ModelCall
	// ChunksEmpty is chunks that held no text to embed. They are counted rather
	// than named in Unread: a blank chunk was read fine, and reporting it as
	// unreadable would make a chunker producing rubbish look like an endpoint
	// going down. A high number here says the chunking is wrong.
	ChunksEmpty int
}

// Embed embeds every chunk and returns the vectors in chunk order.
//
// One batch failing does not lose the job: the other batches still run, and the
// chunks in the failed one are named in Unread with the endpoint's own words.
// Every batch failing does fail the job — a result of nothing that took a
// hundred calls is a failure wearing a success's clothes, which is the rule
// pkg/extract follows for the same reason — but the Result is returned
// alongside the error, because §7.2's promise that cost is never hidden is
// least dispensable on the run that spent the most and got the least.
func Embed(ctx context.Context, chunks []alchemy.Chunk, opts Options) (Result, error) {
	// A nil Embedder is not this stage failing loudly. §6 says any of the three
	// models may be nil, and a job that wants a graph and no vectors is a job,
	// not a misconfiguration — the extractor cannot say that about a nil LLM,
	// because an extraction with no model is nothing at all, but a graph with
	// no vectors is the graph. The silence is deliberate and is what makes it
	// distinguishable downstream: no vectors, no Unread, and nothing on the
	// bill, which is exactly what an endpoint that was asked and produced
	// nothing does not look like.
	if opts.Embedder == nil {
		return Result{}, nil
	}
	// No chunks is a fact, not a fault. An empty corpus that errored would make
	// every caller guard for it, and the guard would eventually also swallow
	// the all-failed case below.
	if len(chunks) == 0 {
		return Result{}, nil
	}

	worth, empty := worthEmbedding(chunks)
	batches := batchesOf(worth, opts.BatchSize)
	res, fatal := assemble(run(ctx, batches, opts), opts)
	res.ChunksEmpty = empty
	// The mismatch is reported ahead of a cancel: one is a fact about what the
	// provider returned and the other about when the caller stopped asking, and
	// the one that changes what a reader may trust about the vectors wins.
	if fatal != nil {
		return res, fatal
	}
	// A cancelled run is not a short run. Whatever finished before the cancel is
	// returned — it was paid for and it is real, and the bill is exactly what a
	// caller who cancelled over cost is looking for — but it comes back with the
	// error, because a caller handed the chunks that happened to finish and told
	// nothing would read them as the corpus.
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("embed: cancelled after embedding %d of %d chunks: %w",
			len(res.Vectors), len(chunks), err)
	}
	// Every batch failing fails the job — but only when there was something to
	// embed. A corpus that was entirely blank bought no calls and suffered no
	// outage; calling that a failed run would report the caller's chunking as
	// our endpoint being down.
	if len(worth) > 0 && len(res.Unread) == len(worth) {
		return res, fmt.Errorf("embed: all %d chunks failed to embed, so this run produced nothing: %s",
			len(worth), res.Unread[0].Reason)
	}
	return res, nil
}

// assemble puts the outcomes back together in chunk order. The workers decide
// when a reply arrives; this decides where it goes, and the two decisions never
// meet — that is what keeps the output independent of Concurrency.
func assemble(outcomes []batchOutcome, opts Options) (Result, error) {
	var res Result
	var fatal error
	// width is the dimension the first real vector came back with, and widthOf
	// the chunk it belongs to, so a later disagreement can name both sides.
	width, widthOf := -1, -1
	spend := alchemy.ModelCall{Model: opts.Embedder.Name(), Stage: stage}
	for _, out := range outcomes {
		spend.Calls += out.calls
		spend.Tokens += out.tokens
		switch {
		case out.fatal != nil:
			// Reported once, from the first batch that broke the contract: a
			// provider that is misaligning batches will misalign many, and ten
			// copies of the same sentence is not ten times the information.
			if fatal == nil {
				fatal = out.fatal
			}
		case out.failed != "":
			for _, c := range out.chunks {
				res.Unread = append(res.Unread, unreadChunk(c, out.failed))
			}
		default:
			for i, c := range out.chunks {
				// A vector with no dimensions is not a vector. It is well-formed
				// enough to be stored and searched against, and depending on the
				// index it then matches everything or nothing — wrong in the way
				// that never raises an error anywhere. Reported, not stored.
				if len(out.vectors[i]) == 0 {
					res.Unread = append(res.Unread, unreadChunk(c,
						"the embedder returned an empty vector, which is not an embedding of anything"))
					continue
				}
				// One run, one width. An index takes vectors of a single
				// dimension, so a provider that changed model between batches
				// has produced something no store will accept — and there is
				// nothing in the data to say which width was the one meant.
				// Same shape of question as a short batch, refused the same way.
				if width < 0 {
					width, widthOf = len(out.vectors[i]), c.Index
				} else if len(out.vectors[i]) != width {
					if fatal == nil {
						fatal = fmt.Errorf("embed: %s returned %d dimensions for chunk %d and %d for chunk %d; "+
							"one run's vectors must share a width or no index will hold them",
							opts.Embedder.Name(), width, widthOf, len(out.vectors[i]), c.Index)
					}
					continue
				}
				res.Vectors = append(res.Vectors, alchemy.Vector{
					Chunk:  c.Index,
					Values: out.vectors[i],
					Model:  opts.Embedder.Name(),
				})
			}
		}
	}
	if spend.Calls > 0 {
		res.ModelCalls = []alchemy.ModelCall{spend}
	}
	return res, fatal
}

// worthEmbedding drops the chunks with no text in them and counts what it
// dropped.
//
// A whitespace-only chunk costs a call and returns a vector describing nothing.
// Worse, every blank text embeds to the same place, so a corpus with fifty of
// them grows a dense cluster that a vague query matches perfectly — a wrong
// answer with a citation, which is the failure this whole design is arranged
// against. Skipping is cheap; the count is what keeps it from being silent.
func worthEmbedding(chunks []alchemy.Chunk) ([]alchemy.Chunk, int) {
	out := make([]alchemy.Chunk, 0, len(chunks))
	empty := 0
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) == "" {
			empty++
			continue
		}
		out = append(out, c)
	}
	return out, empty
}

// unreadChunk names one chunk that has no vector, and why. The locator is the
// chunk index and its byte range so a reader can find the text in the original
// rather than being told a number that means something only to us.
func unreadChunk(c alchemy.Chunk, reason string) alchemy.Unread {
	return alchemy.Unread{
		Source:  c.Source,
		Locator: fmt.Sprintf("chunk %d (bytes %d-%d)", c.Index, c.Start, c.End),
		Reason:  reason,
	}
}
