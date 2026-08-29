package chunk

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// TooLargeError is what Whole returns instead of a chunk nothing can read.
// §7.1 chose Whole for "short documents that fit" and wrote down what it does
// when they do not: it fails loudly, not silently. Silence here would mean
// handing an extractor a prompt its model will truncate, and a truncated
// prompt loses relations without anybody being told.
type TooLargeError struct {
	Source string
	// Tokens is the estimated size of the document (see tokens.go — it is an
	// approximation, and the message says so).
	Tokens    int
	MaxTokens int
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("chunk: %s is about %d tokens, over the budget of %d, and strategy %q may not split it; pick another strategy or raise MaxTokens",
		e.Source, e.Tokens, e.MaxTokens, Whole)
}

// splitWhole returns the document as one chunk, or refuses.
func splitWhole(source, text string, opts Options) ([]alchemy.Chunk, error) {
	if n := approxTokens(text); n > opts.MaxTokens {
		return nil, &TooLargeError{Source: source, Tokens: n, MaxTokens: opts.MaxTokens}
	}
	return emit(source, text, string(Whole), "", []span{{start: 0, end: len(text)}}), nil
}
