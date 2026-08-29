package chunk

import (
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// HeadingSplit is the strategy name a chunk carries when the heading strategy
// ran but a section did not fit and had to be cut. §7.1 names this as the cost
// of the strategy — "a long section exceeds any context" — and a chunk that
// still claimed plain "heading" would hide the one thing a reader comparing
// two runs would want to know about it.
const HeadingSplit = "heading+split"

// splitHeading treats a markdown or HTML section as a chunk. A document with
// no headings at all is not an error: the strategy falls back the way Auto
// does, and the chunks say which strategy actually ran.
func splitHeading(source, text string, opts Options) ([]alchemy.Chunk, error) {
	sections := headingSections(text)
	if len(sections) == 0 {
		return splitStructural(source, text, opts)
	}
	var units []span
	cut := false
	for _, sec := range sections {
		if approxQuarters(text[sec.start:sec.end]) <= chunkRoom(opts) {
			units = append(units, sec)
			continue
		}
		// The section is bigger than any chunk may be. Break it at its own
		// paragraphs first — that keeps the cut somewhere a person would have
		// cut — and let the packer fall back to fixed for what is still too big.
		cut = true
		for _, p := range paragraphUnitsIn(text, sec.start, sec.end) {
			p.heading = sec.heading
			p.group = sec.group
			units = append(units, p)
		}
	}
	spans, oversized := packUnits(text, units, opts)
	name := string(Heading)
	if cut || oversized {
		name = HeadingSplit
	}
	return emit(source, text, name, "", spans), nil
}

// headingSections returns one span per section, each starting at its heading
// line and running to the next heading. Text before the first heading is a
// section with no heading rather than text nobody chunks. Nil means the
// document has no headings, which is a fact the caller acts on, not an error.
func headingSections(text string) []span {
	marks := headingMarks(text)
	if len(marks) == 0 {
		return nil
	}
	var sections []span
	add := func(start, end int, title string) {
		if s, ok := trimSpan(text, span{start: start, end: end, heading: title, group: len(sections) + 1}); ok {
			sections = append(sections, s)
		}
	}
	if marks[0].start > 0 {
		add(0, marks[0].start, "")
	}
	for i, m := range marks {
		end := len(text)
		if i+1 < len(marks) {
			end = marks[i+1].start
		}
		add(m.start, end, m.title)
	}
	return sections
}

// mark is one heading occurrence: where its section begins and what it is
// called. Only the immediate heading is recorded, not a breadcrumb of its
// ancestors — Chunk.Heading is "the section this chunk sits under", and a
// synthesised "Parent > Child" string is not something the document said.
type mark struct {
	start int
	title string
}

func headingMarks(text string) []mark {
	var marks []mark
	for i := 0; i < len(text); {
		if text[i] == '#' && atLineStart(text, i) {
			if m, next, ok := markdownHeading(text, i); ok {
				marks = append(marks, m)
				i = next
				continue
			}
		}
		if text[i] == '<' {
			if m, next, ok := htmlHeading(text, i); ok {
				marks = append(marks, m)
				i = next
				continue
			}
		}
		i++
	}
	return marks
}

func atLineStart(text string, i int) bool {
	return i == 0 || text[i-1] == '\n'
}

// markdownHeading matches "#" to "######" followed by whitespace. The space is
// required, so "#42" in prose and a "#define" are not headings. Setext
// headings (a line underlined with === or ---) are not recognised: telling one
// from a horizontal rule needs lookahead this scanner does not do, and a wrong
// guess would silently reorganise a document.
func markdownHeading(text string, i int) (mark, int, bool) {
	level := 0
	j := i
	for j < len(text) && text[j] == '#' && level < 7 {
		level++
		j++
	}
	if level == 0 || level > 6 || j >= len(text) || (text[j] != ' ' && text[j] != '\t') {
		return mark{}, 0, false
	}
	end := strings.IndexByte(text[j:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += j
	}
	title := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(text[j:end]), "#"))
	if title == "" {
		return mark{}, 0, false
	}
	return mark{start: i, title: title}, end, true
}

// htmlHeading matches <h1>..<h6>, attributes and all.
func htmlHeading(text string, i int) (mark, int, bool) {
	if i+3 >= len(text) || (text[i+1] != 'h' && text[i+1] != 'H') {
		return mark{}, 0, false
	}
	level := text[i+2]
	if level < '1' || level > '6' {
		return mark{}, 0, false
	}
	switch text[i+3] {
	case '>', ' ', '\t', '\n', '\r', '/':
	default:
		return mark{}, 0, false
	}
	open := strings.IndexByte(text[i:], '>')
	if open < 0 {
		return mark{}, 0, false
	}
	open += i + 1
	closeTag := "</h" + string(level)
	rel := indexFold(text[open:], closeTag)
	if rel < 0 {
		return mark{}, 0, false
	}
	inner := text[open : open+rel]
	end := open + rel
	if gt := strings.IndexByte(text[end:], '>'); gt >= 0 {
		end += gt + 1
	}
	title := strings.TrimSpace(stripTags(inner))
	if title == "" {
		return mark{}, 0, false
	}
	return mark{start: i, title: title}, end, true
}

func indexFold(s, sub string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(sub))
}

// stripTags reduces a heading's inner HTML to the words a person would read.
// Only the five named entities that actually appear in headings are decoded;
// this is a heading title, not an HTML parser.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for _, pair := range [][2]string{{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&#39;", "'"}, {"&nbsp;", " "}} {
		out = strings.ReplaceAll(out, pair[0], pair[1])
	}
	return strings.Join(strings.Fields(out), " ")
}
