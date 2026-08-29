package chunk

import (
	"unicode/utf8"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// span is a half-open byte range of the source text. Every strategy reduces to
// a list of spans, so the rule that a chunk's text is exactly text[Start:End]
// is enforced in one place instead of six.
type span struct {
	start   int
	end     int
	heading string
	// group marks units the packer may merge with each other and no others. A
	// heading section and a semantic block are boundaries somebody meant; the
	// packer filling out a chunk must not quietly undo them.
	group int
}

// emit turns spans into chunks. headings, when non-nil, overrides a span's own
// heading; strategy is the name that travels into provenance.
func emit(source, text, strategy, heading string, spans []span) []alchemy.Chunk {
	chunks := make([]alchemy.Chunk, 0, len(spans))
	for _, s := range spans {
		if s.start >= s.end {
			continue
		}
		h := s.heading
		if h == "" {
			h = heading
		}
		chunks = append(chunks, alchemy.Chunk{
			Index:    len(chunks),
			Text:     text[s.start:s.end],
			Source:   source,
			Strategy: strategy,
			Heading:  h,
			Start:    s.start,
			End:      s.end,
		})
	}
	return chunks
}

func runeQuarters(r rune) int {
	if r < utf8.RuneSelf {
		return asciiQuarters
	}
	return nonASCIIQuarters
}

func lastRune(s string) (rune, int) {
	r, size := utf8.DecodeLastRuneInString(s)
	return r, size
}

// trimSpan drops leading and trailing whitespace from a span, reporting false
// when nothing is left. Offsets stay byte offsets into the original text.
func trimSpan(text string, s span) (span, bool) {
	for s.start < s.end && isSpaceByte(text[s.start]) {
		s.start++
	}
	for s.end > s.start && isSpaceByte(text[s.end-1]) {
		s.end--
	}
	return s, s.start < s.end
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}
