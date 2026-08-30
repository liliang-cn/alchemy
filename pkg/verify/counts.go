package verify

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// count fills the block §5b calls "the number that makes the difference
// between a graph you can act on and one you merely have". A run with 1180
// edges and 400 violations is a failure that looks like a success, and without
// this nobody would know.
//
// The deterministic/inferred split is over relations only, matching §5b's
// example (890 + 290 = 1180 edges), and it is computed rather than accumulated
// so it cannot drift away from the slices it describes. Producer.Deterministic
// defaults to false, so a producer invented later is counted as inferred —
// which is the safe direction to be wrong in.
func count(r Report) alchemy.Counts {
	c := alchemy.Counts{
		Entities:   len(r.Entities),
		Relations:  len(r.Relations),
		Violations: len(r.Violations),
		Conflicts:  len(r.Conflicts),
		Duplicates: len(r.Duplicates),
		// Guesses, ChunksEmpty and ChunksUnread are owned by the mapping and
		// chunking stages. This one cannot see them, and a verifier that wrote a
		// zero it had not computed would make the whole block untrustworthy.
	}
	for _, rel := range r.Relations {
		if rel.Provenance.Producer.Deterministic() {
			c.Deterministic++
		}
	}
	c.Inferred = c.Relations - c.Deterministic
	return c
}
