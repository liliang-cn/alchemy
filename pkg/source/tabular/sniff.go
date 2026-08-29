package tabular

import (
	"bufio"
	"fmt"
	"strings"
	"unicode"
)

// candidateDelimiters are the four separators worth guessing between. Anything
// else the caller states.
var candidateDelimiters = []rune{',', '\t', ';', '|'}

// sniffWindow is how much of the head of the file the sniff looks at. It is a
// window rather than the file because §8.4 forbids holding a source whole, and
// because a delimiter that only becomes visible on line 900,000 is not one.
const sniffWindow = 64 << 10

// sniffRecords is how many records the candidates must agree on.
const sniffRecords = 10

// sniff decides the delimiter, or refuses.
//
// A wrong delimiter is the quiet failure of this package's other half: it does
// not error, it produces one enormous column, and every row then "runs cleanly"
// while meaning nothing. So a candidate wins only by being the single one that
// splits the sampled records into the same number of fields every time. Two
// candidates that both do is not a preference to be settled by an order written
// into this file — it is the same ambiguity §2.1 is about, and it is reported.
func sniff(br *bufio.Reader) (rune, error) {
	head, err := br.Peek(sniffWindow)
	if len(head) == 0 {
		return 0, fmt.Errorf("the source is empty, so it has neither a delimiter nor a header row")
	}
	// A short peek means the whole source is in hand, so its last record is
	// complete rather than cut off by the window.
	complete := err != nil
	var viable, seen []rune
	for _, d := range candidateDelimiters {
		counts := recordCounts(string(head), d, sniffRecords, complete)
		if len(counts) == 0 || counts[0] <= 1 {
			continue
		}
		seen = append(seen, d)
		if same(counts) {
			viable = append(viable, d)
		}
	}
	switch {
	case len(viable) == 1:
		return viable[0], nil
	case len(viable) > 1:
		return 0, fmt.Errorf("the delimiter is ambiguous: %s each split this file consistently, and reading it under the wrong one produces a table that means nothing without failing; supply Options.Delimiter",
			names(viable))
	case len(seen) > 0:
		return 0, fmt.Errorf("no delimiter splits this file consistently: %s appear in the header but not in every row; supply Options.Delimiter",
			names(seen))
	default:
		// None of the candidates occurs at all, so every one of them reads the
		// file as the same single column. There is nothing here to get wrong.
		return ',', nil
	}
}

// recordCounts is how many fields each of the first records would have under d.
//
// It counts records rather than lines because a quoted field may contain a
// newline: counting physical lines makes a perfectly well formed file look
// inconsistent under every candidate, and the reader would then refuse a table
// it can read. Blank lines contribute nothing, and a final record with no
// terminator is dropped unless the whole source is in hand — otherwise its
// field count is an artefact of where the window happened to end.
func recordCounts(s string, d rune, max int, complete bool) []int {
	var counts []int
	n, inQuote, started := 1, false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			started = true
		case inQuote:
			// Everything inside quotes is content, newline included.
		case r == '\n':
			if started {
				counts = append(counts, n)
				if len(counts) >= max {
					return counts
				}
			}
			n, started = 1, false
		case r == '\r':
			// Part of a CRLF terminator, never a field of its own.
		case r == d:
			n++
			started = true
		case !unicode.IsSpace(r):
			started = true
		}
	}
	if complete && started {
		counts = append(counts, n)
	}
	return counts
}

func same(counts []int) bool {
	for _, c := range counts {
		if c != counts[0] {
			return false
		}
	}
	return true
}

func names(rs []rune) string {
	var out []string
	for _, r := range rs {
		if r == '\t' {
			out = append(out, `'\t'`)
			continue
		}
		out = append(out, fmt.Sprintf("%q", string(r)))
	}
	return strings.Join(out, " and ")
}
