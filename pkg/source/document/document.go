// Package document turns a document into text a chunker can split.
//
// It reads PDFs with a text layer, markdown, plain text and HTML. Its one
// non-negotiable obligation comes from DESIGN.md §5: a page that could not be
// read is reported in Result.Unread with its page number and a reason. It is
// never returned as empty text, and raw bytes are never coerced into a string.
// harness-rs shipped the other behaviour — pdf-extract returned nothing for a
// scan and the fallback pushed raw PDF bytes through a lossy UTF-8 conversion —
// and the result was an OCR that looked like it worked.
package document

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// ErrNotText is returned for bytes that match no signature this package reads
// and are not valid UTF-8. It is deliberately an error and not an empty
// Result: a caller that receives "" cannot tell a refusal from a blank file.
var ErrNotText = errors.New("not a document this reader can read: the bytes are neither a PDF nor valid UTF-8 text")

// Result is what one document produced.
type Result struct {
	// Text is the document's text, ready for pkg/chunk.
	Text string
	// Unread names the pages or sections that could not be read, and why.
	// Empty Text with a non-empty Unread is the honest report of a scan; empty
	// Text with an empty Unread means the document really is blank.
	Unread []alchemy.Unread
	// ModelCalls records the OCR calls this document cost. §7.2: cost is not
	// optimised for, but it is never hidden.
	ModelCalls []alchemy.ModelCall
}

// Read reads r as a document named source. ocr may be nil, in which case a page
// with no text layer is reported unread rather than guessed at.
//
// source is metadata: it names the document in errors and in Unread, and its
// extension is consulted only when the bytes are ambiguous (see detect).
func Read(ctx context.Context, source string, r io.Reader, ocr alchemy.OCR) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("read %s: no reader", source)
	}
	// Checked here rather than in each reader: a cancelled caller is not
	// waiting for the answer, so not even the first read should happen.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// Detection needs the first bytes; the readers need all of them. Peeking
	// through a bufio.Reader is how both happen without buffering the document
	// twice or seeking a stream that may not be seekable.
	br := bufio.NewReaderSize(r, sniffLen*4)
	prefix, err := br.Peek(sniffLen)
	if err != nil && err != io.EOF && !errors.Is(err, bufio.ErrBufferFull) {
		return Result{}, fmt.Errorf("read %s: %w", source, err)
	}
	switch detect(source, prefix) {
	case formatPDF:
		return readPDF(ctx, source, br, ocr)
	case formatHTML:
		return readHTML(ctx, source, br)
	case formatMarkdown, formatText:
		return readText(ctx, source, br)
	default:
		return Result{}, fmt.Errorf("read %s: %w", source, ErrNotText)
	}
}

// cancellable makes a read stop at the first chunk after cancellation. io.Copy
// has no context, and a 10GB source is exactly the case where noticing matters.
type cancellable struct {
	ctx context.Context
	r   io.Reader
}

func (c cancellable) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
