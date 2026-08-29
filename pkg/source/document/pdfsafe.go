package document

import (
	"bufio"
	"errors"
	"io"

	pdflib "github.com/ledongthuc/pdf"
)

// Proving a page's streams will not hang the interpreter before running it.
//
// The PDF library's content-stream lexer returns a synthetic newline forever
// once the stream is exhausted, and its hex-string reader skips newlines as
// whitespace while waiting for ">". An unterminated hex string therefore spins
// a core for as long as the process lives — a panic can be recovered from, but
// this cannot, so it has to be prevented rather than caught.
//
// The check is a scan of the same streams the interpreter will read, in one
// pass, holding nothing: it tracks whether a "<" hex string is still open at
// end of input. Literal strings and comments are tracked only so that a "<"
// inside one is not mistaken for the start of a hex string.
//
// It costs one extra decompression per page. That is the price of a reader a
// customer's file cannot hang, which is not a close call.

// errHangingStream is the reason a page is reported unread when its streams
// would not terminate.
var errHangingStream = errors.New("a stream ends inside an unterminated hex string, which would hang the PDF interpreter")

// pageIsSafe reports whether every stream this page's text extraction will
// interpret terminates. Fonts are included because the library interprets a
// font's ToUnicode CMap with the same lexer.
func pageIsSafe(page pdflib.Page) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errHangingStream
		}
	}()
	if err := streamTerminates(page.V.Key("Contents")); err != nil {
		return err
	}
	for _, name := range page.Fonts() {
		if err := streamTerminates(page.Font(name).V.Key("ToUnicode")); err != nil {
			return err
		}
	}
	return nil
}

// streamTerminates checks one stream, or every stream of an array — the shape
// a page's /Contents is allowed to take.
func streamTerminates(v pdflib.Value) error {
	if v.Kind() == pdflib.Array {
		for i := 0; i < v.Len(); i++ {
			if err := streamTerminates(v.Index(i)); err != nil {
				return err
			}
		}
		return nil
	}
	rd := v.Reader()
	defer rd.Close()
	if hexStringLeftOpen(rd) {
		return errHangingStream
	}
	return nil
}

// scan states. Only the constructs that can swallow a "<" or a ">" need to be
// tracked; everything else is one byte at a time in stateNormal.
const (
	stateNormal = iota
	stateComment
	stateLiteral
	stateHex
)

// hexStringLeftOpen reports whether the stream ends while a hex string is open.
// A read error counts as the end of the stream: a truncated stream that stops
// mid-hex-string is exactly the case this exists to catch.
func hexStringLeftOpen(r io.Reader) bool {
	br := bufio.NewReader(r)
	state := stateNormal
	depth := 0
	for {
		c, err := br.ReadByte()
		if err != nil {
			return state == stateHex
		}
		switch state {
		case stateNormal:
			switch c {
			case '%':
				state = stateComment
			case '(':
				state, depth = stateLiteral, 1
			case '<':
				// "<<" opens a dictionary; a lone "<" opens a hex string.
				if next, err := br.Peek(1); err == nil && next[0] == '<' {
					br.ReadByte()
				} else {
					state = stateHex
				}
			case '>':
				if next, err := br.Peek(1); err == nil && next[0] == '>' {
					br.ReadByte()
				}
			}
		case stateComment:
			if c == '\r' || c == '\n' {
				state = stateNormal
			}
		case stateLiteral:
			switch c {
			case '\\':
				// The escaped byte is consumed whatever it is, so that "\)"
				// does not close the string.
				br.ReadByte()
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					state = stateNormal
				}
			}
		case stateHex:
			if c == '>' {
				state = stateNormal
			}
		}
	}
}
