package review

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// rank is §5c's table as a number. It is a constant ordering rather than a
// score, because the reasons behind it are not commensurable: a conflict is a
// question no amount of data answers, a violation is one source breaking a
// declared rule, a guess misaligns a whole table (§2.1), and an unsure edge is
// one edge. Nothing about a particular item makes an unsure edge outrank a
// conflict.
const (
	rankConflict = iota
	rankViolation
	rankGuess
	rankDuplicate
	rankLowConfidence
)

// Queue ranks what is worth a person's time.
//
// §7.3's table is the authority on what goes in. Conflicts are queued whether
// or not the caller asked for review, because a conflict holds the job either
// way and the queue is how it gets unblocked; everything else is queued only
// in review mode, because a caller who did not ask for review asked for the
// service §5b describes and gets it. The one addition to that table is a
// duplicate somebody has already answered — see the loop below for why an
// answer, unlike a question, has to be carried on the nights nobody is
// watching.
//
// Nothing here is computed. Every item points at a finding the verifier or a
// mapper already produced, which is §5c's "what is worth reviewing is already
// computed" taken literally: a review stage that re-analysed the graph would
// be a second opinion with no provenance of its own.
func Queue(rep verify.Report, res alchemy.Result, opts Options) []Item {
	idx := index(rep.Entities, rep.Relations)
	var out []Item

	for i, c := range rep.Conflicts {
		out = append(out, conflictItem(i, c, idx))
	}

	if opts.Reviewing {
		for i, v := range rep.Violations {
			out = append(out, violationItem(i, v, idx))
		}
		for i, g := range res.Guesses {
			out = append(out, guessItem(i, g))
		}
	}

	// Duplicates are the one kind that is queued outside review mode without
	// being a conflict, and only the ones somebody has already answered.
	//
	// Everywhere else a rule reaches an unattended run through the extractor,
	// which settles each chunk's proposal as it arrives (see extract.Settled).
	// A duplicate cannot go that way: it is a fact about two chunks, and no
	// chunk can see it. So the answer has to be carried here or nowhere, and
	// "nowhere" would mean an operator's written merge policy applies on the
	// nights a person is watching and not on the nights they are not — which
	// is the opposite of what a policy is for.
	//
	// An unanswered one stays out. It is a question, and §5c's default is that
	// a caller who did not ask for review is not asked anything; unattended,
	// what they get is the finding and the count, exactly as for a violation.
	for i, d := range rep.Duplicates {
		it := duplicateItem(i, d, idx)
		if opts.Reviewing || ruleFor(it, opts.Rules) != nil {
			out = append(out, it)
		}
	}

	if opts.Reviewing {
		out = append(out, lowConfidence(rep, opts.MinConfidence, covered(out))...)
	}

	return finish(out, opts.Rules)
}

// finish attaches the rule that already answered each item, and numbers them.
//
// Rank is assigned last and is the position in the queue rather than the kind,
// so a reviewer working top to bottom and a caller sorting by Rank see the
// same order. Suppressed items keep their place: they are answers, and an
// answer that changed the numbering of the questions around it would make a
// half-worked queue impossible to resume.
func finish(items []Item, rules []Rule) []Item {
	if len(items) == 0 {
		return nil
	}
	for i := range items {
		items[i].Rank = i
		items[i].SuppressedBy = ruleFor(items[i], rules)
	}
	dedupeIDs(items)
	return items
}

// dedupeIDs makes IDs unique without making them positional. An ID built from
// content is what lets a decision made against yesterday's queue still name
// the same item today; appending the position instead would renumber every
// item below a resolved one.
func dedupeIDs(items []Item) {
	seen := make(map[string]int, len(items))
	for i := range items {
		id := items[i].ID
		seen[id]++
		if n := seen[id]; n > 1 {
			items[i].ID = fmt.Sprintf("%s#%d", id, n)
		}
	}
}

func conflictItem(i int, c alchemy.Conflict, idx *records) Item {
	// The Right claim is the dissenting one: verify records the incumbent —
	// and, for a contradiction, the deterministic side — on the left. So a
	// decision on this item is a decision about the newcomer, which is the
	// question the reviewer is actually being asked.
	targets, attr := idx.find(c.Subject, c.Right.Provenance)
	return Item{
		ID:         fmt.Sprintf("conflict/%s/%s", c.Kind, c.Subject),
		Kind:       KindConflict,
		Index:      i,
		Subject:    c.Subject,
		Summary:    c.Detail,
		Shape:      conflictShape(c, attr),
		Provenance: c.Right.Provenance,
		Targets:    targets,
	}
}

func violationItem(i int, v alchemy.Violation, idx *records) Item {
	targets, _ := idx.find(v.Subject, v.Provenance)
	return Item{
		ID:         fmt.Sprintf("violation/%s/%s", v.Kind, v.Subject),
		Kind:       KindViolation,
		Index:      i,
		Subject:    v.Subject,
		Summary:    v.Detail,
		Shape:      violationShape(v, targets),
		Provenance: v.Provenance,
		Targets:    targets,
	}
}

// guessItem has no targets, and that is a fact about mappings rather than an
// omission. A guess says a column was read as a field; the records it produced
// carry no back-reference to it, and inventing one by matching provenance
// would sweep in every record that shared a source. Apply refuses the verbs
// that would need one.
func guessItem(i int, g alchemy.Guess) Item {
	subject := guessSubject(g)
	return Item{
		ID:      "guess/" + subject,
		Kind:    KindGuess,
		Index:   i,
		Subject: subject,
		Summary: fmt.Sprintf("%q was mapped to %q%s%s", g.Field, g.ChosenAs,
			over(g.Alternatives), because(g.Reason)),
		Shape:      guessShape(g),
		Provenance: g.Provenance,
	}
}

// duplicateItem asks the one question a duplicate raises: are these two nodes
// one node?
//
// Both nodes are targets, and neither is the offender. A conflict has an
// incumbent and a dissenter, so its item acts on the newcomer; here nobody
// arrived second at anything, which is the defect itself — the two proposals
// never met. Which of the two moves is what the reviewer says with Edit.Into,
// and Apply refuses an Into that is not one of these two.
//
// The Provenance shown is the right side's, matching every other two-sided
// item: it is the side whose spelling is the longer one, which is the record a
// reader is usually being asked about.
func duplicateItem(i int, d alchemy.Duplicate, idx *records) Item {
	left, _ := idx.find(d.Left.ID, d.Left.Provenance)
	right, _ := idx.find(d.Right.ID, d.Right.Provenance)
	return Item{
		ID:         fmt.Sprintf("duplicate/%s/%s", d.Signal, d.Subject),
		Kind:       KindDuplicate,
		Index:      i,
		Subject:    d.Subject,
		Summary:    d.Detail,
		Shape:      duplicateShape(d),
		Provenance: d.Right.Provenance,
		Targets:    append(left, right...),
	}
}

func over(alternatives []string) string {
	if len(alternatives) == 0 {
		return ""
	}
	return " over " + strings.Join(quoted(alternatives), ", ")
}

func because(reason string) string {
	if reason == "" {
		return ""
	}
	return "; " + reason
}

func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

// lowConfidence queues records the model itself flagged.
//
// Two records never reach here. A deterministic one is §5c's last row and its
// stated reason — asking a person to confirm what a CREATE TABLE says is how
// you teach them to click Approve without reading. And a record some
// higher-ranked item already names is left to that item: two queue entries
// about one edge is the same person answering the same question twice, and the
// second answer is the one they stop reading.
func lowConfidence(rep verify.Report, min float64, taken map[Ref]bool) []Item {
	if min <= 0 {
		return nil
	}
	var out []Item
	for _, e := range rep.Entities {
		ref := entityRef(e)
		if !unsure(e.Provenance, min) || taken[ref] {
			continue
		}
		out = append(out, Item{
			ID:      fmt.Sprintf("low_confidence/entity/%s", e.ID),
			Kind:    KindLowConfidence,
			Subject: e.ID,
			Summary: fmt.Sprintf("entity %q was typed %q with confidence %.2f by %s",
				e.ID, e.Type, e.Provenance.Confidence, model(e.Provenance)),
			Shape:      lowConfidenceShape(string(RefEntity), e.Type, e.Provenance),
			Provenance: e.Provenance,
			Targets:    []Ref{ref},
		})
	}
	for _, r := range rep.Relations {
		ref := relationRef(r)
		if !unsure(r.Provenance, min) || taken[ref] {
			continue
		}
		out = append(out, Item{
			ID:      fmt.Sprintf("low_confidence/relation/%s", directed(r)),
			Kind:    KindLowConfidence,
			Subject: directed(r),
			Summary: fmt.Sprintf("%s was proposed with confidence %.2f by %s",
				directed(r), r.Provenance.Confidence, model(r.Provenance)),
			Shape:      lowConfidenceShape(string(RefRelation), r.Type, r.Provenance),
			Provenance: r.Provenance,
			Targets:    []Ref{ref},
		})
	}
	return out
}

// unsure is deliberately narrow. §5c's row is "the model was unsure and said
// so", and a Confidence of zero is not a model saying it was unsure — it is a
// producer that reports no confidence at all, including every deterministic
// one. Treating silence as doubt would put the whole graph in the queue.
func unsure(p alchemy.Provenance, min float64) bool {
	return !p.Producer.Deterministic() && p.Confidence > 0 && p.Confidence < min
}

func model(p alchemy.Provenance) string {
	if p.Model == "" {
		return string(p.Producer)
	}
	return p.Model
}

func covered(items []Item) map[Ref]bool {
	taken := map[Ref]bool{}
	for _, it := range items {
		for _, ref := range it.Targets {
			taken[ref] = true
		}
	}
	return taken
}

// guessSubject names a guess the way a queue item does. It is one function
// rather than two because Apply compares the two to prove it was handed the
// result the queue was built from, and a second spelling would make that
// comparison a coin flip.
func guessSubject(g alchemy.Guess) string {
	if g.Provenance.Source != "" {
		return g.Provenance.Source + ":" + g.Field
	}
	return g.Field
}
