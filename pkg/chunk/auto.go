package chunk

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// splitAuto is the default of §7.1: heading, falling back to paragraph,
// falling back to fixed — "because most of what people import has structure and
// ignoring it is the one choice that is wrong for every corpus rather than
// some".
//
// It never labels a chunk "auto". The fallback is a decision the pipeline made
// on the caller's behalf, and a decision nobody can see afterwards is a guess
// that does not announce itself.
func splitAuto(source, text string, opts Options) ([]alchemy.Chunk, error) {
	if len(headingSections(text)) > 0 {
		return splitHeading(source, text, opts)
	}
	return splitStructural(source, text, opts)
}

// splitStructural is the lower two rungs of the ladder. One paragraph is not
// structure — a document with no blank line in it has told us nothing, so the
// baseline runs and says it ran.
func splitStructural(source, text string, opts Options) ([]alchemy.Chunk, error) {
	if len(paragraphUnitsIn(text, 0, len(text))) > 1 {
		return splitParagraph(source, text, opts)
	}
	return splitFixed(source, text, opts)
}
