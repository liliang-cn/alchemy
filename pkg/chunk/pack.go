package chunk

// chunkRoom is how much of the budget a chunk's own units may fill. The
// overlap is spent out of the budget, not added to it: a chunk that repeats
// the tail of its predecessor must still fit what the caller said it would fit.
func chunkRoom(opts Options) int {
	return tokensToQuarters(opts.MaxTokens - opts.Overlap)
}

// packUnits packs units — sentences, paragraphs, sections, semantic blocks —
// into spans that fit the budget, and reports whether any single unit was too
// big to keep whole. That second return value exists because a strategy that
// silently hands back an oversized chunk has broken the only promise the
// budget makes; the caller of packUnits says so in the strategy name instead.
func packUnits(text string, units []span, opts Options) ([]span, bool) {
	back := tokensToQuarters(opts.Overlap)
	room := chunkRoom(opts)

	var out []span
	oversized := false
	cur := span{start: -1}
	curQ := 0
	flush := func() {
		if cur.start >= 0 {
			out = append(out, cur)
			cur = span{start: -1}
			curQ = 0
		}
	}
	for _, u := range units {
		q := approxQuarters(text[u.start:u.end])
		if q > room {
			flush()
			// One unit larger than any chunk can be. Fall through to the fixed
			// baseline for this unit only, keeping its heading on every part.
			// Cut to the room, not to the whole budget: withOverlap is about
			// to spend the rest of it on the repeated tail.
			for _, s := range fixedSpans(text, u.start, u.end, room, 0) {
				s.heading = u.heading
				s.group = u.group
				out = append(out, s)
			}
			oversized = true
			continue
		}
		if cur.start >= 0 && (curQ+q > room || cur.group != u.group || cur.heading != u.heading) {
			flush()
		}
		if cur.start < 0 {
			cur = span{start: u.start, end: u.end, heading: u.heading, group: u.group}
			curQ = q
			continue
		}
		cur.end = u.end
		curQ += q
	}
	flush()
	return withOverlap(text, out, back), oversized
}

// withOverlap moves each span's start back into its predecessor. §7.1: a
// relation cut in half by a boundary is recovered when the next chunk starts
// before the previous one ended.
func withOverlap(text string, spans []span, quarters int) []span {
	if quarters <= 0 {
		return spans
	}
	for i := 1; i < len(spans); i++ {
		prev := spans[i-1]
		if spans[i].start <= prev.start {
			continue
		}
		floor := prev.start + 1
		spans[i].start = retreat(text, spans[i].start, quarters, floor)
	}
	return spans
}
