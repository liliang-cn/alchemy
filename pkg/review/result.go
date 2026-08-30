package review

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// stampFindings records that a person answered a report entry.
//
// The finding stays in the result rather than being deleted. §5b's obligation
// is that a graph reports its own quality, and a conflict that vanishes
// because somebody decided it leaves a result that looks like it never had
// one — which is the same result an unreviewed clean job produces, and the two
// are not the same thing. A decided conflict whose claims say "reviewed by
// ana" is a job that was held, asked, and answered, and it reads that way
// months later.
func stampFindings(res *alchemy.Result, decided []answered) error {
	for _, a := range decided {
		by := a.decision.By
		switch a.item.Kind {
		case KindConflict:
			if a.item.Index >= len(res.Conflicts) {
				return outOfRange(a.item, "conflicts")
			}
			c := res.Conflicts[a.item.Index]
			if err := sameFinding(a.item, "conflicts", c.Subject); err != nil {
				return err
			}
			// Both sides are stamped. The reviewer did not approve one claim;
			// they adjudicated between two, and a Left that stayed unmarked
			// would read as the side nobody considered.
			c.Left.Provenance.ReviewedBy = reviewedBy(c.Left.Provenance.ReviewedBy, by)
			c.Right.Provenance.ReviewedBy = reviewedBy(c.Right.Provenance.ReviewedBy, by)
			res.Conflicts = replace(res.Conflicts, a.item.Index, c)
		case KindViolation:
			if a.item.Index >= len(res.Violations) {
				return outOfRange(a.item, "violations")
			}
			v := res.Violations[a.item.Index]
			if err := sameFinding(a.item, "violations", v.Subject); err != nil {
				return err
			}
			v.Provenance.ReviewedBy = reviewedBy(v.Provenance.ReviewedBy, by)
			res.Violations = replace(res.Violations, a.item.Index, v)
		case KindDuplicate:
			if a.item.Index >= len(res.Duplicates) {
				return outOfRange(a.item, "duplicates")
			}
			d := res.Duplicates[a.item.Index]
			if err := sameFinding(a.item, "duplicates", d.Subject); err != nil {
				return err
			}
			// Both sides, as for a conflict, and here it is load-bearing
			// rather than symmetric. If the answer was a merge, one of these
			// two nodes is no longer in the graph, and this finding is the
			// only place its provenance survives: the chunk that proposed the
			// other spelling, and the name of whoever decided the two were one
			// thing. §5b's promise is that a record is attributable, and a
			// merge that erased the absorbed node's origin would spend that
			// promise to buy a tidier graph.
			d.Left.Provenance.ReviewedBy = reviewedBy(d.Left.Provenance.ReviewedBy, by)
			d.Right.Provenance.ReviewedBy = reviewedBy(d.Right.Provenance.ReviewedBy, by)
			res.Duplicates = replace(res.Duplicates, a.item.Index, d)
		case KindGuess:
			if a.item.Index >= len(res.Guesses) {
				return outOfRange(a.item, "guesses")
			}
			g := res.Guesses[a.item.Index]
			if err := sameFinding(a.item, "guesses", guessSubject(g)); err != nil {
				return err
			}
			g.Provenance.ReviewedBy = reviewedBy(g.Provenance.ReviewedBy, by)
			res.Guesses = replace(res.Guesses, a.item.Index, g)
		}
	}
	return nil
}

// outOfRange names the invariant that was broken rather than panicking on it:
// Apply must be given the result the queue was built from. Queue reads
// conflicts and violations out of the verifier's report and guesses out of the
// result, and a coordinator that assembled the two into a Result kept their
// order, because the verifier is deterministic.
// sameFinding refuses to stamp a finding that is not the one the item
// described.
//
// The range check above catches a result that is shorter than the queue. This
// catches the one that is the right length and holds different findings —
// which is the worse of the two, because it succeeds: a reviewer's name lands
// on something they never saw, and the finding they did answer comes back
// unmarked. §5c's claim is "the model proposed, and what you have was
// checked"; a name on the wrong finding makes that claim false in the one way
// nobody can see afterwards.
//
// Subject is what it compares because it is the field Queue copies off the
// finding, so the two agree exactly when the item and the finding are the same
// thing.
func sameFinding(item Item, slice, subject string) error {
	if subject == item.Subject {
		return nil
	}
	return fmt.Errorf("review: item %q describes %q but %s[%d] is now %q; Apply needs the result the queue was built from, in the order it was built from",
		item.ID, item.Subject, slice, item.Index, subject)
}

func outOfRange(item Item, slice string) error {
	return fmt.Errorf("review: item %q points at %s[%d], which this result does not have; Apply needs the result the queue was built from", item.ID, slice, item.Index)
}

// replace copies on first write. A caller holding the pending result while a
// reviewer works must not see its findings change under it, and every one of
// these slices is shared with the input until something is actually decided.
func replace[T any](in []T, i int, v T) []T {
	out := make([]T, len(in))
	copy(out, in)
	out[i] = v
	return out
}

// recount refills the block §5b calls the difference between a graph you can
// act on and one you merely have. It is recomputed rather than adjusted,
// because a count maintained by increments drifts away from the slices it
// describes exactly once and then lies forever.
//
// The chunk numbers are carried over untouched: they belong to the chunking
// stage, and review neither reads chunks nor removes them. Rejecting an edge
// does not turn its chunk into an empty one — the chunk still produced
// something, and a person threw it away afterwards.
func recount(res alchemy.Result, prior alchemy.Counts, dropped int) alchemy.Counts {
	c := alchemy.Counts{
		Entities:  len(res.Entities),
		Relations: len(res.Relations),
		// Recomputed rather than carried, unlike the chunk numbers below:
		// review neither adds nor removes a chunk or a vector, so these two are
		// the same either way — and computing them keeps the rule that a number
		// derivable from the slices is derived, so a count and its subject
		// cannot drift apart in the one place a reviewer edited the graph.
		Chunks:     len(res.Chunks),
		Vectors:    len(res.Vectors),
		Violations: len(res.Violations),
		Conflicts:  len(res.Conflicts),
		Guesses:    len(res.Guesses),
		// Duplicates counts the findings and not the pairs still unjoined, so
		// it does not fall when a merge is applied. That is the honest number:
		// a graph in which somebody merged three pairs is a graph three
		// questions were asked and answered about, and a count that erased
		// them would make it indistinguishable from one where the extractor
		// never stuttered. Which of them were answered is on each finding, in
		// the reviewer's name.
		Duplicates:   len(res.Duplicates),
		ChunksEmpty:  prior.ChunksEmpty,
		ChunksUnread: prior.ChunksUnread,
		// Dropped accumulates rather than being recomputed, for the same
		// reason ChunksEmpty is carried: a record a rule removed leaves
		// nothing behind to count. It is the one number here that a later
		// reader cannot derive from the slices next to it, which is exactly
		// why it has to be carried honestly.
		Dropped: prior.Dropped + dropped,
	}
	for _, r := range res.Relations {
		if r.Provenance.Producer.Deterministic() {
			c.Deterministic++
		}
	}
	c.Inferred = c.Relations - c.Deterministic
	return c
}

// rulesFrom turns every `always` into the rule it meant.
//
// §5c: "A rule is recorded with the decision that produced it, so a later
// reader can see why the rule exists." The whole Decision travels — who, what
// verb, what note — along with the sentence the reviewer was reading when they
// made it, because the item is gone once the job expires and a rule whose
// origin has expired with it is back to being an unexplainable policy.
//
// Two `always` decisions about one shape produce one rule. The second adds no
// coverage, and a rule list that grows a duplicate every time the same class
// is answered again is one nobody can read.
func rulesFrom(decided []answered) []Rule {
	var out []Rule
	seen := map[string]bool{}
	for _, a := range decided {
		if a.decision.Verb != VerbAlways || a.item.Shape == "" || seen[a.item.Shape] {
			continue
		}
		seen[a.item.Shape] = true
		out = append(out, Rule{
			Shape:   a.item.Shape,
			Kind:    a.item.Kind,
			From:    a.decision,
			Because: a.item.Summary,
		})
	}
	return out
}
