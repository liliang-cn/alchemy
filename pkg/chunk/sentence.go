package chunk

import (
	"unicode"
	"unicode/utf8"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// splitSentence packs whole sentences up to the budget. §7.1 states the cost:
// a fact spanning a paragraph can still split, which is what the overlap is
// there for.
func splitSentence(source, text string, opts Options) ([]alchemy.Chunk, error) {
	spans, _ := packUnits(text, sentenceUnits(text), opts)
	return emit(source, text, string(Sentence), "", spans), nil
}

// sentenceUnits finds sentence boundaries without a language model and without
// an abbreviation list, so it is a heuristic and gets some things wrong: an
// ASCII full stop followed by a space ends a sentence here even when it was
// "e.g. " or "Dr. Watson". The failure mode is a boundary in the wrong place,
// which the overlap already exists to soften — an abbreviation dictionary would
// be a per-language asset this package has no way to keep honest.
func sentenceUnits(text string) []span {
	var units []span
	start := 0
	for i, r := range text {
		if i < start || !isTerminator(r) {
			continue
		}
		end := i + utf8.RuneLen(r)
		// A sentence may end in several marks at once — "really?!" — and the
		// closing quote or bracket belongs to the sentence it closes.
		for end < len(text) {
			next, size := utf8.DecodeRuneInString(text[end:])
			if !isTerminator(next) && !isCloser(next) {
				break
			}
			end += size
		}
		// An ASCII terminator ends a sentence only when whitespace follows;
		// otherwise "3.14" and "v1.2" become boundaries. CJK terminators need
		// no such guard because CJK text does not space its sentences.
		if r < utf8.RuneSelf && end < len(text) {
			if next, _ := utf8.DecodeRuneInString(text[end:]); !unicode.IsSpace(next) {
				continue
			}
		}
		if u, ok := trimSpan(text, span{start: start, end: end}); ok {
			units = append(units, u)
		}
		start = end
	}
	if u, ok := trimSpan(text, span{start: start, end: len(text)}); ok {
		units = append(units, u)
	}
	return units
}

func isTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？', '…':
		return true
	}
	return false
}

func isCloser(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '”', '’', '）', '】', '」', '』':
		return true
	}
	return false
}
