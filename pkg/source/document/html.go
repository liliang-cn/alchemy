package document

import (
	"context"
	"io"
	"strings"
)

// readHTML turns HTML into text with its heading structure intact.
//
// The output shape is markdown ATX headings ("## Section"), not the original
// <h2> tags. pkg/chunk's heading scanner recognises both, so either would be
// "visible" to it — markdown is chosen because everything else in the output is
// tag-free prose, and leaving lone <h2> tags in it would make the heading the
// only markup in the document, which an extractor reading a chunk would have to
// know to ignore. It also means HTML and markdown reach the chunker in one
// shape rather than two.
//
// This is a text extractor, not an HTML parser: it does not build a tree, does
// not correct nesting, and does not care that <p> and <li> are left unclosed —
// all three are normal in real pages, and none of them change where the text is.
func readHTML(ctx context.Context, source string, r io.Reader) (Result, error) {
	res, err := readText(ctx, source, r)
	if err != nil {
		return Result{}, err
	}
	res.Text = htmlToText(res.Text)
	return res, nil
}

// rawText are elements whose contents are not text. Their bodies are code,
// styling or document chrome; a page's <title> is chrome too, and inventing a
// heading out of it would be structure the document did not state.
var rawText = map[string]bool{
	"script": true, "style": true, "noscript": true,
	"template": true, "title": true, "iframe": true, "svg": true,
}

// blockTags separate paragraphs. A missing entry costs a paragraph break, not
// correctness, so the list is the elements that actually appear in prose.
var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "header": true,
	"footer": true, "main": true, "aside": true, "nav": true, "blockquote": true,
	"ul": true, "ol": true, "dl": true, "dt": true, "dd": true, "pre": true,
	"table": true, "thead": true, "tbody": true, "tr": true, "hr": true,
	"figure": true, "figcaption": true, "form": true, "fieldset": true,
	"address": true, "details": true, "summary": true, "body": true,
}

func htmlToText(src string) string {
	w := &htmlWriter{}
	for i := 0; i < len(src); {
		c := src[i]
		if c != '<' {
			j := strings.IndexByte(src[i:], '<')
			if j < 0 {
				j = len(src) - i
			}
			w.text(decodeEntities(src[i : i+j]))
			i += j
			continue
		}
		// "<!--" is a comment, "<!" anything else is a doctype or a CDATA
		// marker; neither is text.
		if strings.HasPrefix(src[i:], "<!--") {
			i = skipPast(src, i+4, "-->")
			continue
		}
		if strings.HasPrefix(src[i:], "<!") || strings.HasPrefix(src[i:], "<?") {
			i = skipPast(src, i+2, ">")
			continue
		}
		name, closing, end, ok := parseTag(src, i)
		if !ok {
			// A "<" that begins no tag is literal text, which is what a page
			// with an unescaped comparison operator in it contains.
			w.text("<")
			i++
			continue
		}
		if !closing && rawText[name] {
			i = skipElement(src, end, name)
			continue
		}
		w.tag(name, closing)
		i = end
	}
	return strings.TrimSpace(w.String()) + "\n"
}

// parseTag reads one tag starting at src[i] == '<'. It returns the lowercased
// element name, whether it is a closing tag, and the offset just past '>'.
// Attribute values are scanned with their quoting respected, because a href
// containing ">" is not the end of the tag.
func parseTag(src string, i int) (name string, closing bool, end int, ok bool) {
	j := i + 1
	if j < len(src) && src[j] == '/' {
		closing = true
		j++
	}
	start := j
	for j < len(src) && isTagNameByte(src[j]) {
		j++
	}
	if j == start {
		return "", false, 0, false
	}
	name = strings.ToLower(src[start:j])
	for j < len(src) {
		switch src[j] {
		case '"', '\'':
			q := src[j]
			j++
			for j < len(src) && src[j] != q {
				j++
			}
		case '>':
			return name, closing, j + 1, true
		}
		j++
	}
	// An unterminated tag at end of file: everything left is markup, not text.
	return name, closing, len(src), true
}

func isTagNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == ':' || b == '_'
}

func skipPast(src string, from int, delim string) int {
	if k := strings.Index(src[from:], delim); k >= 0 {
		return from + k + len(delim)
	}
	return len(src)
}

// skipElement drops everything up to the element's closing tag. An unclosed
// <script> swallows the rest of the document, which is exactly what a browser
// does with it.
func skipElement(src string, from int, name string) int {
	closing := "</" + name
	rest := strings.ToLower(src[from:])
	k := strings.Index(rest, closing)
	if k < 0 {
		return len(src)
	}
	return skipPast(src, from+k, ">")
}

// htmlWriter assembles the text. Breaks are requested rather than written, so
// that a run of nested block tags produces one paragraph break and a break
// before any text at all produces nothing.
type htmlWriter struct {
	out     []byte
	pending int // 0 none, 1 newline, 2 blank line
	prefix  string
	heading int // heading level currently open, 0 when not in one
	pre     int // depth of open <pre> elements
}

func (w *htmlWriter) String() string { return string(w.out) }

func (w *htmlWriter) tag(name string, closing bool) {
	if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
		w.block()
		if closing {
			w.heading = 0
		} else {
			w.heading = int(name[1] - '0')
			w.prefix = strings.Repeat("#", w.heading) + " "
		}
		return
	}
	if name == "pre" {
		w.block()
		if closing {
			if w.pre > 0 {
				w.pre--
			}
		} else {
			w.pre++
		}
		return
	}
	switch name {
	case "br":
		w.line()
	case "li":
		if !closing {
			w.block()
			w.prefix = "- "
		}
	case "td", "th":
		if !closing {
			w.gap()
		}
	default:
		if blockTags[name] {
			w.block()
		}
	}
}

// block asks for a paragraph break. Inside a heading it degrades to a space:
// a heading has to stay on one line or pkg/chunk stops seeing it as a heading.
func (w *htmlWriter) block() {
	if w.heading > 0 {
		w.gap()
		return
	}
	if w.pending < 2 {
		w.pending = 2
	}
}

func (w *htmlWriter) line() {
	if w.heading > 0 {
		w.gap()
		return
	}
	if w.pending < 1 {
		w.pending = 1
	}
}

// gap separates two cells or two inline runs without starting a line.
func (w *htmlWriter) gap() {
	if w.pending > 0 || len(w.out) == 0 {
		return
	}
	if last := w.out[len(w.out)-1]; last != ' ' && last != '\n' {
		w.out = append(w.out, ' ')
	}
}

// text writes one decoded text node, collapsing runs of whitespace. Collapsing
// is safe here in a way it would not be for markdown: HTML whitespace between
// tags is layout, not content, and pkg/chunk's paragraph strategy needs blank
// lines rather than the source's indentation.
func (w *htmlWriter) text(s string) {
	if s == "" {
		return
	}
	// Inside <pre> the whitespace is the content: it is where a code block
	// keeps its line breaks and its indentation.
	if w.pre > 0 {
		if strings.TrimSpace(s) == "" && len(w.out) == 0 {
			return
		}
		w.flush()
		w.out = append(w.out, s...)
		return
	}
	collapsed := collapseSpaces(s)
	if collapsed == " " {
		w.gap()
		return
	}
	leading := strings.HasPrefix(collapsed, " ")
	collapsed = strings.TrimLeft(collapsed, " ")
	if collapsed == "" {
		return
	}
	if leading {
		w.gap()
	}
	w.flush()
	w.out = append(w.out, collapsed...)
}

// flush emits the requested break and any pending list marker or heading
// prefix. Nothing is emitted until real text arrives, so an empty heading or an
// empty list item leaves no marker behind.
func (w *htmlWriter) flush() {
	if w.pending > 0 {
		w.trimTrailingSpace()
		if len(w.out) > 0 {
			w.out = append(w.out, '\n')
			if w.pending == 2 {
				w.out = append(w.out, '\n')
			}
		}
		w.pending = 0
	}
	if w.prefix != "" {
		w.out = append(w.out, w.prefix...)
		w.prefix = ""
	}
}

func (w *htmlWriter) trimTrailingSpace() {
	for len(w.out) > 0 && (w.out[len(w.out)-1] == ' ' || w.out[len(w.out)-1] == '\t') {
		w.out = w.out[:len(w.out)-1]
	}
}

// collapseSpaces reduces every run of whitespace to one space, keeping one
// leading and one trailing space as a marker that a word boundary was there.
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			} else if space {
				b.WriteByte(' ')
			}
			space = false
			b.WriteByte(c)
		}
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}
