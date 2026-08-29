package chunk

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// splitFixed cuts every MaxTokens with a fixed overlap. It respects rune
// boundaries and nothing else — that is the whole point of the baseline, and
// §7.1 states the cost in the same breath as the strategy: it cuts mid-sentence
// and mid-fact.
func splitFixed(source, text string, opts Options) ([]alchemy.Chunk, error) {
	budget := tokensToQuarters(opts.MaxTokens)
	back := tokensToQuarters(opts.Overlap)
	return emit(source, text, string(Fixed), "", fixedSpans(text, 0, len(text), budget, back)), nil
}

// fixedSpans cuts [from,to) into spans of at most budget quarter-tokens, each
// starting back quarter-tokens before the last one ended. The overlap is taken
// out of the following chunk's window rather than added to it, so no chunk is
// ever larger than the budget the caller set.
func fixedSpans(text string, from, to, budget, back int) []span {
	var spans []span
	for start := from; start < to; {
		end := advance(text, start, to, budget)
		spans = append(spans, span{start: start, end: end})
		if end >= to {
			break
		}
		start = retreat(text, end, back, start+1)
	}
	return spans
}

// advance returns the byte offset reached by spending quarters of budget from
// start, never splitting a rune and never passing limit. It stops before the
// rune that would take it over budget rather than after it — overshooting by a
// rune is how a chunk ends up one token above a limit somebody chose for a
// reason.
func advance(text string, start, limit, quarters int) int {
	spent := 0
	for i, r := range text[start:limit] {
		w := runeQuarters(r)
		if spent+w > quarters && i > 0 {
			return start + i
		}
		spent += w
	}
	return limit
}

// retreat walks back from end by quarters worth of runes, never before floor.
// It is how a chunk starts before the previous one ended.
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
