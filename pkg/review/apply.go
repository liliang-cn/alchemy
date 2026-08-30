package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Apply carries a set of decisions onto a result.
//
// A set, not a script: §5c's reviewer works a queue, and two people working
// two halves of one queue must not produce two different graphs depending on
// whose answers arrived first. So the decisions are collected into a plan
// keyed by the records they act on, and the graph is then walked once in its
// own order. Nothing here reads decisions in sequence, which is why the
// order-independence test can hold.
//
// What comes back is a new Result. The input is not modified — a caller
// holding the pending result while a reviewer works through it would otherwise
// watch it change underneath them.
func Apply(res alchemy.Result, items []Item, decisions []Decision) (alchemy.Result, []Rule, error) {
	byID, err := indexItems(items)
	if err != nil {
		return alchemy.Result{}, nil, err
	}
	decided, err := resolve(items, byID, decisions)
	if err != nil {
		return alchemy.Result{}, nil, err
	}
	plan, err := newPlan(decided)
	if err != nil {
		return alchemy.Result{}, nil, err
	}
	out, err := plan.run(res, decided)
	if err != nil {
		return alchemy.Result{}, nil, err
	}
	return out, rulesFrom(decided), nil
}

func indexItems(items []Item) (map[string]Item, error) {
	byID := make(map[string]Item, len(items))
	for _, it := range items {
		if _, dup := byID[it.ID]; dup {
			return nil, fmt.Errorf("review: two queue items share the id %q", it.ID)
		}
		byID[it.ID] = it
	}
	return byID, nil
}

// answered pairs a decision with the item it answers, which is the unit
// everything below works on: neither half is meaningful alone.
type answered struct {
	item     Item
	decision Decision
	// byRule says the answer came from a standing rule rather than from
	// somebody looking at the item. It is only used to count what a rule
	// removed (alchemy.Counts.Dropped) — a record a person threw away was
	// thrown away by somebody who saw it, and a record a written policy
	// removed before any queue was shown is the one this design can otherwise
	// lose without a number anywhere.
	byRule bool
}

// resolve matches decisions to items and refuses the two ways a caller can be
// wrong about what it is answering. It then fills in the items a rule already
// answered, so that §7.3's unattended pipeline gets its conflicts resolved
// rather than merely hidden: a rule that kept an item out of sight without
// deciding it would leave a job held on a question nobody is being shown.
//
// An explicit decision on a suppressed item wins over its rule. Somebody
// looked at it anyway, and a person beats a policy — this is not order
// dependence, because explicit decisions are collected first and by item, not
// by position.
//
// An unknown item ID is an error rather than a no-op because it means the
// reviewer and the service disagree about what was asked, and the failure mode
// of ignoring it is a result that claims to be reviewed while the item the
// reviewer thought they had rejected is still in the graph.
//
// Two different decisions on one item is also an error, and this is the one
// place where "last wins" was seriously considered and rejected. Last-wins
// needs an order, and the order of decisions is exactly what this function
// promises not to matter; a set that changes its answer depending on how it
// was shuffled is not a set. Beyond the mechanics, a package whose entire
// subject is that contradictions get asked rather than silently settled cannot
// silently settle its own. A redelivered identical decision is not a
// contradiction and is accepted: Review is a bidirectional stream (§6), and
// streams that reconnect redeliver.
func resolve(items []Item, byID map[string]Item, decisions []Decision) ([]answered, error) {
	seen := make(map[string]Decision, len(decisions))
	var out []answered
	for _, d := range decisions {
		item, ok := byID[d.ItemID]
		if !ok {
			return nil, fmt.Errorf("review: decision names item %q, which is not in the queue it was given", d.ItemID)
		}
		if prior, dup := seen[d.ItemID]; dup {
			if !prior.sameAs(d) {
				return nil, fmt.Errorf("review: item %q has two different decisions (%s by %s, %s by %s); decisions are a set, so there is no later one to prefer",
					d.ItemID, prior.Verb, prior.By, d.Verb, d.By)
			}
			continue
		}
		if err := check(item, d); err != nil {
			return nil, err
		}
		seen[d.ItemID] = d
		out = append(out, answered{item: item, decision: d})
	}
	for _, it := range items {
		if it.SuppressedBy == nil {
			continue
		}
		if _, asked := seen[it.ID]; asked {
			continue
		}
		d := it.SuppressedBy.From
		d.ItemID = it.ID
		if err := check(it, d); err != nil {
			return nil, fmt.Errorf("rule %q: %w", it.SuppressedBy.Shape, err)
		}
		out = append(out, answered{item: it, decision: d, byRule: true})
	}
	// Sorted by item ID so that everything downstream — error messages
	// included — reads the same whichever order the decisions arrived in.
	sort.Slice(out, func(i, j int) bool { return out[i].item.ID < out[j].item.ID })
	return out, nil
}

// check refuses decisions that cannot mean what they say.
func check(item Item, d Decision) error {
	switch d.Verb {
	case VerbAccept, VerbReject, VerbEdit, VerbAlways:
	default:
		return fmt.Errorf("review: item %q was given the unknown verb %q", d.ItemID, d.Verb)
	}
	// §5c: an accepted graph carries who accepted what. A decision nobody
	// signed cannot be written into provenance, and "reviewed by" with nobody
	// in it claims a review that has no one behind it.
	if d.By == "" {
		return fmt.Errorf("review: decision on item %q names nobody; a review with no reviewer is not a review", d.ItemID)
	}
	if d.Verb == VerbEdit && (d.Edit == nil || d.Edit.empty()) {
		return fmt.Errorf("review: item %q was edited with no change; a record marked reviewed and left alone is an accept wearing the wrong label", d.ItemID)
	}
	if item.Kind == KindGuess && (d.Verb == VerbReject || d.Verb == VerbEdit) {
		// A guess is a mapping, not a record. The rows it produced carry no
		// back-reference to it, so there is nothing here to delete or retype;
		// the correction is a re-import with the mapping stated. Refusing is
		// the point — a reviewer who was told their edit landed, when nothing
		// in the graph moved, is worse off than one who was told it cannot.
		return fmt.Errorf("review: item %q is an inferred mapping, which %s cannot act on; re-import with the mapping stated instead", d.ItemID, d.Verb)
	}
	if len(item.Targets) == 0 && d.Verb != VerbAccept && d.Verb != VerbAlways && item.Kind != KindGuess {
		return fmt.Errorf("review: item %q names no record in this result, so %s has nothing to act on", d.ItemID, d.Verb)
	}
	return nil
}

// plan is the decisions expressed as facts about records rather than as a
// list of instructions. Building it is what makes Apply order-independent.
type plan struct {
	remove map[Ref]bool
	edit   map[Ref]Edit
	stamp  map[Ref]string
	// asked is the records somebody was actually asked about. A record in
	// remove but not in asked was removed by a rule alone, which is what
	// Counts.Dropped reports; a person's explicit decision on the same record
	// takes it out of that number, because then somebody did read it.
	asked map[Ref]bool
	// dropped is how many records the walk removed on a rule's word alone.
	dropped int
}

func newPlan(decided []answered) (*plan, error) {
	p := &plan{remove: map[Ref]bool{}, edit: map[Ref]Edit{}, stamp: map[Ref]string{}, asked: map[Ref]bool{}}
	for _, a := range decided {
		for _, ref := range a.item.Targets {
			p.stamp[ref] = a.decision.By
			if !a.byRule {
				p.asked[ref] = true
			}
			switch a.decision.Verb {
			case VerbReject:
				p.remove[ref] = true
			case VerbEdit, VerbAlways:
				if a.decision.Edit == nil {
					continue
				}
				if prior, dup := p.edit[ref]; dup && prior != *a.decision.Edit {
					return nil, fmt.Errorf("review: two items edit the record %s differently (%+v and %+v)", describe(ref), prior, *a.decision.Edit)
				}
				p.edit[ref] = *a.decision.Edit
			}
		}
	}
	// A record both rejected and edited is settled in favour of the rejection
	// rather than by whichever decision was seen last: a record that is gone
	// cannot also be corrected, and the reviewer who removed it made the
	// stronger statement about it.
	for ref := range p.remove {
		delete(p.edit, ref)
	}
	return p, nil
}

func describe(r Ref) string {
	if r.Kind == RefEntity {
		return fmt.Sprintf("entity %q", r.ID)
	}
	return fmt.Sprintf("%s -[%s]-> %s", r.From, r.Type, r.To)
}

// run walks the graph once, in its own order, and produces the reviewed one.
func (p *plan) run(res alchemy.Result, decided []answered) (alchemy.Result, error) {
	out := res
	gone := map[string]bool{}

	out.Entities = make([]alchemy.Entity, 0, len(res.Entities))
	for _, e := range res.Entities {
		ref := entityRef(e)
		if p.remove[ref] {
			gone[e.ID] = true
			p.count(ref)
			continue
		}
		if ed, ok := p.edit[ref]; ok {
			e = editEntity(e, ed)
		}
		if by, ok := p.stamp[ref]; ok {
			e.Provenance.ReviewedBy = reviewedBy(e.Provenance.ReviewedBy, by)
		}
		out.Entities = append(out.Entities, e)
	}

	out.Relations = make([]alchemy.Relation, 0, len(res.Relations))
	for _, r := range res.Relations {
		ref := relationRef(r)
		// An edge left behind by a rejected endpoint is removed with it. The
		// reviewer said the entity is not real; leaving the edge would turn
		// one rejection into a dangling-relation violation they never made,
		// and §5b's promise is that a violation names a source that said
		// something, not one this stage invented.
		if p.remove[ref] || gone[r.From] || gone[r.To] {
			// An edge removed only because a rule took an endpoint away is
			// counted too: it left the graph on the same rule's word, and a
			// count that reported the entity and not the edge it stranded
			// would understate what the policy did.
			p.count(ref)
			continue
		}
		if ed, ok := p.edit[ref]; ok {
			r = editRelation(r, ed)
		}
		if by, ok := p.stamp[ref]; ok {
			r.Provenance.ReviewedBy = reviewedBy(r.Provenance.ReviewedBy, by)
		}
		out.Relations = append(out.Relations, r)
	}

	if err := stampFindings(&out, decided); err != nil {
		return alchemy.Result{}, err
	}
	out.Counts = recount(out, res.Counts, p.dropped)
	return out, nil
}

// count records a removal that no person was asked about.
//
// It is counted here, during the walk, rather than by subtracting lengths
// afterwards: the two are the same number only until an edge is dropped for
// two reasons at once, and a count that has to reason about overlaps is the
// kind that drifts away from its subject and then lies forever.
func (p *plan) count(ref Ref) {
	if !p.asked[ref] {
		p.dropped++
	}
}

// editEntity applies only what the reviewer set. §5c lists "retype an entity
// [or] rename" as separate acts, and a reviewer who did one said nothing about
// the other.
func editEntity(e alchemy.Entity, ed Edit) alchemy.Entity {
	if ed.Type != "" {
		e.Type = ed.Type
	}
	if ed.Name != "" {
		e.Name = ed.Name
	}
	return e
}

func editRelation(r alchemy.Relation, ed Edit) alchemy.Relation {
	if ed.Type != "" {
		r.Type = ed.Type
	}
	if ed.From != "" {
		r.From = ed.From
	}
	if ed.To != "" {
		r.To = ed.To
	}
	return r
}

// reviewedBy adds a reviewer without dropping one. §5c: review adds to
// provenance, it does not overwrite it — and a record that two rounds of
// review touched was looked at by two people, which is worth more than either
// name alone.
func reviewedBy(existing, by string) string {
	if existing == "" {
		return by
	}
	for _, name := range strings.Split(existing, ", ") {
		if name == by {
			return existing
		}
	}
	return existing + ", " + by
}
