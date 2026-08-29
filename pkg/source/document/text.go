package document

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// readText passes markdown and plain text through unchanged.
//
// Unchanged is the requirement, not a shortcut: pkg/chunk's heading strategy
// finds sections by scanning for "#" at the start of a line, so a reader that
// reflowed, re-indented or normalised anything would delete the only structure
// the chunker has.
//
// The bytes are read once, into a strings.Builder whose String is the same
// allocation — no second copy of the document (§8.4).
func readText(ctx context.Context, source string, r io.Reader) (Result, error) {
	var b strings.Builder
	// The copy is cancellable so that a caller who gave up is not made to wait
	// for a very large file to finish arriving.
	if _, err := io.Copy(&b, cancellable{ctx: ctx, r: r}); err != nil {
		return Result{}, fmt.Errorf("read %s: %w", source, err)
	}
	text := b.String()
	// The prefix was sniffed, but a document can be text for a kilobyte and
	// binary after that. Refusing here is what keeps mojibake out of Text.
	if !utf8.ValidString(text) {
		return Result{}, fmt.Errorf("read %s: %w", source, ErrNotText)
	}
	return Result{Text: text}, nil
}
