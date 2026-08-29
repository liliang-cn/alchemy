package chunk

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// splitParagraph splits on blank lines. §7.1 names the cost: chunk sizes come
// out wildly uneven, because the document decided them, not us. The packer
// evens them out only upwards — small paragraphs are packed together, a
// paragraph over the budget is cut by the fixed baseline rather than returned
// oversized.
func splitParagraph(source, text string, opts Options) ([]alchemy.Chunk, error) {
	spans, _ := packUnits(text, paragraphUnitsIn(text, 0, len(text)), opts)
	return emit(source, text, string(Paragraph), "", spans), nil
}

// paragraphUnitsIn returns the blank-line separated runs of text, with the blank
// lines themselves left out: a chunk that opens with three newlines spends
// budget on nothing.
func paragraphUnitsIn(text string, from, to int) []span {
	var units []span
	start := from
	for i := from; i < to; {
		if text[i] != '\n' {
			i++
			continue
		}
		j, newlines := i, 0
	scan:
		for j < to {
			switch text[j] {
			case '\n':
				newlines++
			case ' ', '\t', '\r':
			default:
				break scan
			}
			j++
		}
		if newlines >= 2 {
			if u, ok := trimSpan(text, span{start: start, end: i}); ok {
				units = append(units, u)
			}
			start = j
		}
		i = j
	}
	if u, ok := trimSpan(text, span{start: start, end: to}); ok {
		units = append(units, u)
	}
	return units
}
