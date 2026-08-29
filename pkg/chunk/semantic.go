package chunk

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// ErrNoEmbedder is returned when Semantic is asked for without one. §7.1 lists
// the cost of this strategy as "an embedding pass before extraction"; a nil
// Embedder means the caller cannot pay it, and quietly running a different
// strategy would leave a graph whose provenance names a strategy that never
// ran.
var ErrNoEmbedder = errors.New("chunk: strategy \"semantic\" needs an Embedder")

// semanticSpread is how far below the mean adjacent-block similarity a gap
// must fall to count as a boundary, in standard deviations. It is relative
// rather than an absolute cosine threshold because every embedding model
// spreads its similarities differently, and an absolute number tuned against
// one model silently mis-splits under another.
const semanticSpread = 0.5

// splitSemantic splits where adjacent blocks stop resembling each other. §7.1
// offers it for "corpora with no reliable structure" — where the document's
// own paragraphs and headings are not to be trusted, the embedding is the only
// evidence left about where a subject ends.
func splitSemantic(ctx context.Context, source, text string, opts Options) ([]alchemy.Chunk, error) {
	if opts.Embedder == nil {
		return nil, ErrNoEmbedder
	}
	blocks := semanticBlocks(text)
	if len(blocks) < 2 {
		// Nothing to compare. Fall through to the packer, which still applies
		// the budget; the name stays "semantic" because that is what ran and
		// the absence of a second block is a fact about the document.
		spans, _ := packUnits(text, blocks, opts)
		return emit(source, text, string(Semantic), "", spans), nil
	}

	texts := make([]string, len(blocks))
	for i, b := range blocks {
		texts[i] = text[b.start:b.end]
	}
	vectors, err := opts.Embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("chunk: embedding %d blocks for the semantic strategy: %w", len(texts), err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("chunk: embedder %q returned %d vectors for %d blocks", opts.Embedder.Name(), len(vectors), len(texts))
	}

	// Group blocks into units at the similarity gaps, then let the packer apply
	// the budget: a subject that runs longer than the budget is still cut, and
	// the caller is not handed a chunk no model can read.
	var units []span
	threshold := semanticThreshold(vectors)
	cur := blocks[0]
	cur.group = 1
	for i := 1; i < len(blocks); i++ {
		if isBoundary(vectors, i, threshold) {
			units = append(units, cur)
			cur = blocks[i]
			cur.group = len(units) + 1
			continue
		}
		cur.end = blocks[i].end
	}
	units = append(units, cur)

	spans, _ := packUnits(text, units, opts)
	return emit(source, text, string(Semantic), "", spans), nil
}

// semanticBlocks are the smallest pieces the strategy will not split: the
// document's paragraphs, or its sentences when it has only one paragraph.
func semanticBlocks(text string) []span {
	blocks := paragraphUnitsIn(text, 0, len(text))
	if len(blocks) > 1 {
		return blocks
	}
	if sentences := sentenceUnits(text); len(sentences) > 1 {
		return sentences
	}
	return blocks
}

func isBoundary(vectors [][]float32, i int, threshold float64) bool {
	return cosine(vectors[i-1], vectors[i]) < threshold
}

// semanticThreshold is the mean adjacent similarity less semanticSpread
// standard deviations. On a uniform document the deviation is zero and the
// strict comparison in isBoundary yields no boundaries at all, which is the
// right answer: nothing in the text says where to cut.
func semanticThreshold(vectors [][]float32) float64 {
	sims := make([]float64, 0, len(vectors)-1)
	for i := 1; i < len(vectors); i++ {
		sims = append(sims, cosine(vectors[i-1], vectors[i]))
	}
	mean := 0.0
	for _, s := range sims {
		mean += s
	}
	mean /= float64(len(sims))
	variance := 0.0
	for _, s := range sims {
		variance += (s - mean) * (s - mean)
	}
	variance /= float64(len(sims))
	return mean - semanticSpread*math.Sqrt(variance)
}

// cosine is 0 for a zero vector or a length mismatch — an embedder that says
// nothing about a block says nothing about where its boundaries are, and 0
// simply means "no evidence of resemblance".
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
