package chunk

import (
	"unicode"
	"unicode/utf8"
)

// Cuts that land inside a word.
//
// A chunk boundary is allowed to fall mid-sentence — §7.1 says so about the
// fixed baseline and means it. Mid-*word* is a different thing, and it is not
// a cosmetic one: an extractor is one call per chunk and reads what it is
// given, so a chunk beginning "ph is responsible for LINSTOR-Gateway" is a
// chunk in which somebody named ph is responsible for LINSTOR-Gateway. It
// produced a Person named "ph" in a real load, wired to a real product, and
// nothing downstream could tell it from a fact: the name is not a duplicate of
// "christoph" by any signal duplicates.go is willing to trust (it is a bare
// suffix, and "Ada"/"Nevada" is why that signal is not trusted), it violates
// no rule, and it cites a chunk that really does say it.
//
// So the boundary snaps to a word. Both directions move the cut *inward* —
// a start moves forward, an end moves backward — which is what makes this safe
// to do everywhere: no chunk grows, no budget is exceeded, and the overlap
// only ever gives back a few characters of insurance it was adding anyway.
//
// Scripts that do not put spaces between words are left alone. Han, Hiragana,
// Katakana and Hangul have a boundary at every character, so "snapping" inside
// them would walk forward through the whole run looking for a space that is
// not coming — turning a cut that lost nothing into one that loses a sentence.
// The test for "this looks like a word being broken" is therefore about the
// two runes touching the cut, and both of them have to be the kind of letter
// that spells words with its neighbours.

// wordRune reports whether r spells a word together with the rune beside it.
// Digits and the joiners that appear inside names and identifiers count;
// ideographs and syllabaries do not, for the reason above.
func wordRune(r rune) bool {
	switch r {
	case '_', '-', '\'', '’':
		return true
	}
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
		return false
	}
	return !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// insideWord reports whether the boundary at byte offset i cuts a word in two.
func insideWord(text string, i int) bool {
	if i <= 0 || i >= len(text) {
		return false
	}
	before, _ := lastRune(text[:i])
	after, _ := utf8.DecodeRuneInString(text[i:])
	return wordRune(before) && wordRune(after)
}

// snapStartToWholeWord moves a chunk's start off the middle of a word,
// forward to where the next word begins, never past limit.
//
// Forward rather than back, because back is over budget. The overlap is taken
// out of the following chunk's window precisely so that no chunk is ever
// larger than the budget the caller set, and a start that reached back to the
// beginning of the broken word would spend a few tokens nobody allowed it —
// which chunk_test.go checks for every strategy, and is right to.
//
// The cost is that an overlap smaller than the word it lands in is spent
// getting out of that word, and the boundary ends up with no overlap at all.
// That is the honest answer for such a budget: an overlap of four characters
// cannot carry a six-character word, and carrying four characters *of* it is
// not insurance — it is a fragment that reads as a fact. A caller who wants
// overlap wants enough of it to hold a word.
func snapStartToWholeWord(text string, i, floor, limit int) int {
	_ = floor
	if !insideWord(text, i) {
		return i
	}
	for j := i; j < limit; {
		r, size := utf8.DecodeRuneInString(text[j:])
		if !wordRune(r) {
			return j + size
		}
		j += size
	}
	return i
}

// snapEndBackward moves a chunk's end off the middle of a word, back to where
// the broken word began, never before floor. An end with no whole word after
// floor is left where it was, for the same reason.
func snapEndBackward(text string, i, floor int) int {
	if !insideWord(text, i) {
		return i
	}
	j := i
	for j > floor {
		r, size := lastRune(text[:j])
		if !wordRune(r) {
			return j
		}
		j -= size
	}
	return i
}
