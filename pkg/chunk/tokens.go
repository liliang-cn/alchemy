package chunk

import "unicode/utf8"

// The budget is in tokens but no tokenizer is imported, so tokens are
// estimated. The estimate is deliberately crude: an ASCII rune counts as a
// quarter token, any other rune as a whole one, which is roughly how BPE
// vocabularies behave on English prose and on CJK respectively.
//
// It is NOT exact and must not be read as if it were. A real tokenizer will
// disagree with it, sometimes by 30% on code, dense punctuation or rare
// scripts. The number exists to keep chunks in the neighbourhood of a model's
// context window, not to fill it to the last token; callers who need an exact
// count should leave headroom, which is why the budget is the caller's to set.
const (
	asciiQuarters    = 1
	nonASCIIQuarters = 4
)

// approxTokens estimates how many tokens s would cost. See the note above: the
// answer is an approximation and rounds up, so a caller is never told a string
// is cheaper than it is.
func approxTokens(s string) int {
	return quartersToTokens(approxQuarters(s))
}

// approxQuarters counts in quarter-tokens, the unit the scanners work in
// because it is integral and so cannot drift as a float sum would.
func approxQuarters(s string) int {
	q := 0
	for _, r := range s {
		if r < utf8.RuneSelf {
			q += asciiQuarters
			continue
		}
		q += nonASCIIQuarters
	}
	return q
}

func quartersToTokens(q int) int {
	return (q + 3) / 4
}

func tokensToQuarters(t int) int {
	return t * 4
}
