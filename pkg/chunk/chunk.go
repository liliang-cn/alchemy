// Package chunk splits source text into alchemy.Chunk values.
//
// DESIGN.md §7.1 makes chunking a job input rather than a detail: chunk
// boundaries decide what an extractor can see at once, and a relation whose two
// ends land in different chunks is a relation nobody extracts. The strategies
// are therefore named and the caller picks; this package's job is to run the
// one it was given honestly and to record which one ran.
package chunk

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Strategy is one of the six named in §7.1, plus Auto for the default.
type Strategy string

const (
	// Fixed splits every N tokens. The predictable baseline; cuts mid-fact.
	Fixed Strategy = "fixed"
	// Sentence packs whole sentences up to the budget.
	Sentence Strategy = "sentence"
	// Paragraph splits on blank lines.
	Paragraph Strategy = "paragraph"
	// Heading treats a markdown or HTML section as a chunk.
	Heading Strategy = "heading"
	// Semantic splits where adjacent blocks stop resembling each other.
	Semantic Strategy = "semantic"
	// Whole does not split, and says so loudly when the document does not fit.
	Whole Strategy = "whole"
	// Auto is the default of §7.1: heading, falling back to paragraph, falling
	// back to fixed. It never appears on a chunk — see Split.
	Auto Strategy = "auto"
)

// Defaults. MaxTokens is a conservative section size rather than any specific
// model's window, because the model is the caller's and is not known here.
const (
	DefaultMaxTokens = 1000
	// DefaultOverlapDivisor makes the default overlap a tenth of the budget.
	// §7.1: overlap is non-zero by default because it is the cheap insurance
	// against the split-relation problem.
	DefaultOverlapDivisor = 10
)

// Options are the chunking inputs of one job.
type Options struct {
	// Strategy is the strategy to run. The zero value means Auto.
	Strategy Strategy
	// MaxTokens is the budget per chunk, estimated (see tokens.go). Zero means
	// DefaultMaxTokens.
	MaxTokens int
	// Overlap is how many tokens of the previous chunk the next one repeats.
	// Zero means the default, which is non-zero; a caller who genuinely wants
	// no overlap must say so with NoOverlap, so that losing the insurance is
	// always a decision somebody made.
	Overlap int
	// Embedder is required by Semantic and ignored by every other strategy.
	Embedder alchemy.Embedder
}

// NoOverlap turns overlap off explicitly. It is negative rather than zero
// because zero is how a caller says "whatever you think", and what this package
// thinks is that overlap should be on.
const NoOverlap = -1

// Split runs the strategy over text and returns its chunks.
//
// Every returned chunk satisfies c.Text == text[c.Start:c.End], and carries the
// strategy that actually ran: a chunk produced under Auto says "heading", never
// "auto", because "auto" tells a reader comparing two runs nothing.
func Split(ctx context.Context, source, text string, opts Options) ([]alchemy.Chunk, error) {
	opts, err := opts.normalise()
	if err != nil {
		return nil, err
	}
	switch opts.Strategy {
	case Fixed:
		return splitFixed(source, text, opts)
	default:
		return nil, fmt.Errorf("chunk: unknown strategy %q", opts.Strategy)
	}
}

func (o Options) normalise() (Options, error) {
	if o.Strategy == "" {
		o.Strategy = Auto
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = DefaultMaxTokens
	}
	if o.MaxTokens < 0 {
		return o, fmt.Errorf("chunk: MaxTokens must be positive, got %d", o.MaxTokens)
	}
	switch {
	case o.Overlap == 0:
		o.Overlap = o.MaxTokens / DefaultOverlapDivisor
	case o.Overlap < 0:
		o.Overlap = 0
	}
	// An overlap at or above the budget cannot make progress: the next chunk
	// would start where the last one did. Refuse rather than quietly clamp,
	// because a clamped budget is a lie about what ran.
	if o.Overlap >= o.MaxTokens {
		return o, fmt.Errorf("chunk: Overlap (%d tokens) must be below MaxTokens (%d)", o.Overlap, o.MaxTokens)
	}
	return o, nil
}
