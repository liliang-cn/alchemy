package chunk

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// splitFixed cuts every MaxTokens with a fixed overlap. It respects rune
// boundaries and nothing else — that is the whole point of the baseline, and
// §7.1 states the cost in the same breath as the strategy: it cuts mid-sentence
// and mid-fact.
func splitFixed(source, text string, opts Options) ([]alchemy.Chunk, error) {
	return emit(source, text, string(Fixed), "", fixedSpans(text, opts)), nil
}

func fixedSpans(text string, opts Options) []span {
	var spans []span
	budget := tokensToQuarters(opts.MaxTokens)
	back := tokensToQuarters(opts.Overlap)
	for start := 0; start < len(text); {
		end := advance(text, start, budget)
		spans = append(spans, span{start: start, end: end})
		if end >= len(text) {
			break
		}
		start = retreat(text, end, back, start+1)
	}
	return spans
}

// advance returns the byte offset reached by spending quarters of budget from
// start, never splitting a rune.
func advance(text string, start, quarters int) int {
	spent := 0
	for i, r := range text[start:] {
		if spent >= quarters {
			return start + i
		}
		spent += runeQuarters(r)
	}
	return len(text)
}

// retreat walks back from end by quarters worth of runes, never before floor.
func retreat(text string, end, quarters, floor int) int {
	if quarters <= 0 {
		return end
	}
	spent := 0
	i := end
	for i > floor {
		r, size := lastRune(text[:i])
		if spent+runeQuarters(r) > quarters {
			break
		}
		spent += runeQuarters(r)
		i -= size
	}
	if i < floor {
		return floor
	}
	return i
}
