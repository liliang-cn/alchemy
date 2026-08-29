package document

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// format is which reader handles a document.
type format string

const (
	formatPDF      format = "pdf"
	formatHTML     format = "html"
	formatMarkdown format = "markdown"
	formatText     format = "text"
	// formatUnknown is bytes that are not text and match no signature. It is a
	// refusal, not a fallback: the one thing this package must never do is push
	// unrecognised bytes through a lossy conversion and call the result text.
	formatUnknown format = "unknown"
)

// sniffLen is how much of the document detection looks at. A signature that is
// not in the first kilobyte is not a signature.
const sniffLen = 1024

// detect decides which reader handles a document.
//
// Precedence: content first, extension second.
//
// The extension is a label the caller chose; the bytes are what we have to
// parse. A .txt holding a PDF is a real thing, and trusting the label would
// send PDF bytes down the text path — which is the shape of the bug in §5.
// So a recognised content signature always wins.
//
// The extension decides only what the bytes cannot. Markdown and plain text
// are the same bytes — nothing in the content tells them apart — and an HTML
// fragment with no <html> or doctype has no signature either. Where the
// extension is silent or unknown, text is the assumption, because a document
// source that refused every unfamiliar extension would be useless.
//
// Bytes that match no signature and are not valid UTF-8 are formatUnknown.
func detect(source string, prefix []byte) format {
	if hasPDFSignature(prefix) {
		return formatPDF
	}
	if hasHTMLSignature(prefix) {
		return formatHTML
	}
	if !looksTextual(prefix) {
		return formatUnknown
	}
	switch strings.ToLower(filepath.Ext(source)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return formatMarkdown
	case ".html", ".htm", ".xhtml":
		return formatHTML
	default:
		return formatText
	}
}

// hasPDFSignature requires the header at offset 0. Some producers leave junk in
// front of it, but no PDF parser worth using will read such a file anyway, and
// a "%PDF-" found further in is more likely to be a code block in prose.
func hasPDFSignature(prefix []byte) bool {
	return strings.HasPrefix(string(prefix), "%PDF-")
}

// hasHTMLSignature looks for the markers a document uses to declare itself
// HTML. A bare <div> or <p> is not one of them: markdown may contain either,
// and misfiling markdown as HTML would strip the headings pkg/chunk needs.
func hasHTMLSignature(prefix []byte) bool {
	head := strings.ToLower(strings.TrimLeft(string(prefix), " \t\r\n\ufeff"))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// looksTextual reports whether the sniffed prefix can be read as text. A NUL
// byte settles it — no text format this package reads contains one — and
// anything that is not valid UTF-8 is refused rather than transliterated.
func looksTextual(prefix []byte) bool {
	if len(prefix) == 0 {
		return true
	}
	for _, b := range prefix {
		if b == 0x00 {
			return false
		}
	}
	// The window may end mid-rune, so an incomplete rune at the tail is not
	// evidence of anything. Drop up to three trailing bytes to find a boundary.
	trimmed := prefix
	for i := 0; i < utf8.UTFMax && len(trimmed) > 0; i++ {
		if utf8.Valid(trimmed) {
			return true
		}
		trimmed = trimmed[:len(trimmed)-1]
	}
	return false
}
