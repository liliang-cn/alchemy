package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	pdflib "github.com/ledongthuc/pdf"
	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// readPDF extracts a PDF's text page by page.
//
// The PDF format is random-access — the cross-reference table at the end names
// byte offsets throughout the file — so a parser needs io.ReaderAt and cannot
// work from a stream. DESIGN.md §8.4 forbids holding the source in memory, so
// the bytes are spooled to a temp file that is removed before this returns.
func readPDF(ctx context.Context, source string, r io.Reader, ocr alchemy.OCR) (Result, error) {
	f, size, err := spool(ctx, r)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			// A cancelled caller gets the cancellation, not a report about the
			// half-copied file it caused.
			return Result{}, err
		}
		return Result{}, fmt.Errorf("read %s: %w", source, err)
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()

	doc, err := openPDF(f, size)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", source, err)
	}

	pageCount, err := numPages(doc)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", source, err)
	}

	jpegs := &jpegFinder{f: f, size: size}
	var res Result
	var pages []string
	calls := 0
	for i := 1; i <= pageCount; i++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		text, perr := pageText(doc, i)
		switch {
		case perr != nil:
			// A page whose content stream will not parse is a different fact
			// from a page with no text on it, and OCR is the answer to the
			// second one only. Saying which is what makes the report useful.
			res.unread(source, i, "page could not be parsed: "+perr.Error())
		case strings.TrimSpace(text) != "":
			pages = append(pages, text)
		default:
			if recognised, ok := ocrPage(ctx, &res, source, i, doc, jpegs, ocr, &calls); ok {
				pages = append(pages, recognised)
			}
		}
	}
	if calls > 0 {
		res.ModelCalls = append(res.ModelCalls, alchemy.ModelCall{
			Model: ocr.Name(), Stage: "ocr", Calls: calls,
		})
	}
	res.Text = strings.Join(pages, "\n\n")
	return res, nil
}

// ocrPage is the whole of DESIGN.md §5 in one function. A page with no text
// layer has exactly four outcomes and three of them are Unread; none of them is
// an empty string handed back as if it were the page's text.
func ocrPage(ctx context.Context, res *Result, source string, page int,
	doc *pdflib.Reader, jpegs *jpegFinder, ocr alchemy.OCR, calls *int) (string, bool) {

	if ocr == nil {
		res.unread(source, page, "page has no text layer and no OCR model was supplied")
		return "", false
	}
	img, err := extractPageImage(doc, page, jpegs)
	if err != nil {
		res.unread(source, page, "page has no text layer and its image could not be given to OCR: "+err.Error())
		return "", false
	}
	// The attempt is counted before it is made: a call that failed still cost
	// the caller a call, and §7.2 says cost is never hidden.
	*calls++
	text, err := ocr.Recognize(ctx, img.data, img.mediaType)
	if err != nil {
		res.unread(source, page, "page has no text layer and OCR failed: "+err.Error())
		return "", false
	}
	if strings.TrimSpace(text) == "" {
		res.unread(source, page, "page has no text layer and OCR returned no text")
		return "", false
	}
	return text, true
}

// unread appends one unread page. Locator is a page number because that is what
// a person looking at the document in a viewer can act on.
func (res *Result) unread(source string, page int, reason string) {
	res.Unread = append(res.Unread, alchemy.Unread{
		Source:  source,
		Locator: fmt.Sprintf("page %d", page),
		Reason:  reason,
	})
}

// openPDF wraps the library's constructor. It reports malformed input by
// panicking in places, and a source reader that can be crashed by a customer's
// file is a service that can be crashed by a customer's file.
func openPDF(f io.ReaderAt, size int64) (doc *pdflib.Reader, err error) {
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, fmt.Errorf("malformed PDF: %v", r)
		}
	}()
	return pdflib.NewReader(f, size)
}

// numPages counts the pages. Walking to the page tree already touches the
// cross-reference table and the object graph, so a file that is malformed
// anywhere near the root reaches this first — and the library reports malformed
// input by panicking.
func numPages(doc *pdflib.Reader) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = 0, fmt.Errorf("malformed PDF: %v", r)
		}
	}()
	return doc.NumPage(), nil
}

// pageText returns one page's text. An error means the page could not be
// parsed; empty text with no error means the page has no text layer. Keeping
// those two apart is the whole requirement — see the package comment.
func pageText(doc *pdflib.Reader, num int) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("%v", r)
		}
	}()
	page := doc.Page(num)
	if page.V.IsNull() {
		return "", fmt.Errorf("page %d is missing from the page tree", num)
	}
	// Checked before the interpreter runs, because the failure it prevents is
	// an infinite loop and there is no recovering from one.
	if err := pageIsSafe(page); err != nil {
		return "", err
	}
	return page.GetPlainText(nil)
}

// spool copies r to a temp file so the PDF parser can seek in it. The copy is
// cancellable: §8.4's 10GB source is exactly the one a caller gives up on.
func spool(ctx context.Context, r io.Reader) (*os.File, int64, error) {
	f, err := os.CreateTemp("", "alchemy-document-*.pdf")
	if err != nil {
		return nil, 0, err
	}
	n, err := io.Copy(f, cancellable{ctx: ctx, r: r})
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, 0, err
	}
	return f, n, nil
}
