package review

import (
	"fmt"
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// This file is what happens after somebody answers "yes, these two are one
// thing" — the only decision in this package that changes which node a record
// belongs to rather than what a record says.
//
// It is small on purpose, and most of it is about what must not be left
// behind. A merge that removes a node and leaves its edges pointing at an id
// nothing carries is worse than no merge at all: it turns one person's
// judgement into a dangling-relation violation they never made, which is the
// same failure Apply already refuses when a rejected entity strands an edge.

// resolveMerges turns the decisions that fold one node into another into the
// map the walk reads, and refuses the two ways a set of them can fail to mean
// anything.
//
// Two decisions sending one node to two different survivors is a contradiction
// and is refused rather than settled by order — the same argument resolve makes
// for two different decisions on one item. A cycle is refused for a plainer
// reason: there is no node left standing at the end of it.
//
// Chains are not refused. Three spellings of one package is exactly what the
// run this was written from produced, and answering two findings — "long is
// short" and "longer is long" — is a reviewer being consistent, not a reviewer
// being wrong. So the chain is followed to the node that nothing absorbs, and
// every member lands on it.
func (p *plan) resolveMerges() error {
	direct := map[string]string{}
	for ref, ed := range p.edit {
		if ed.Into == "" || ref.Kind != RefEntity {
			continue
		}
		if prior, dup := direct[ref.ID]; dup && prior != ed.Into {
			return fmt.Errorf("review: entity %q is merged into both %q and %q; a record can only be one node, and there is no later decision to prefer",
				ref.ID, prior, ed.Into)
		}
		direct[ref.ID] = ed.Into
	}
	// Sorted, so that a corpus with a cycle in it names the same node in the
	// error every time it is run. Map order would make the message a coin flip
	// and the failure unreproducible.
	from := make([]string, 0, len(direct))
	for id := range direct {
		from = append(from, id)
	}
	sort.Strings(from)
	for _, id := range from {
		to, err := follow(direct, id)
		if err != nil {
			return err
		}
		p.absorb[id] = to
	}
	return nil
}

// silentHalf reports that this record is the survivor of the merge rather than
// the node being folded into it.
//
// Both nodes of a pair are targets, because the reviewer was shown both and
// both are owed their name in the provenance. Only one of them moves. Without
// this the survivor would be recorded as edited into itself, which says nothing
// — and a node that survives one pair and is absorbed by the next, which is
// what three spellings of one thing produces, would look like one record with
// two contradictory edits on it.
func silentHalf(ref Ref, ed Edit) bool {
	return ed.Into != "" && ref.Kind == RefEntity && ref.ID == ed.Into
}

// follow walks a chain of merges to the node at the end of it.
//
// The step limit is the cycle detector: a chain through n nodes cannot take
// more than n steps, so an n+1th step means the walk has come back on itself.
func follow(direct map[string]string, from string) (string, error) {
	to := from
	for i := 0; i <= len(direct); i++ {
		next, ok := direct[to]
		if !ok {
			return to, nil
		}
		to = next
	}
	return "", fmt.Errorf("review: the merges starting at %q come back round to it; every node in that cycle would be absorbed and none would be left to hold the edges", from)
}

// survivor is the id a record's end now points at. It is the identity for
// anything nobody merged, which is almost everything.
func (p *plan) survivor(id string) string {
	if into, ok := p.absorb[id]; ok {
		return into
	}
	return id
}

// absorbedAttributes collects what the absorbed nodes stated, so the survivor
// can carry it.
//
// Unioned rather than replaced, for the reason merger.add already gives one
// package over: two records describing one thing usually state different things
// about it, and keeping only one side's would throw away the reason the other
// chunk was read. Where both state a key the survivor's value stands and the
// absorbed one is not silently substituted; what was lost is not lost from the
// result, because the finding stays in it carrying both nodes and both
// provenances.
//
// The walk is in graph order, so two absorbed nodes offering one key resolve
// the same way in every run.
func (p *plan) absorbedAttributes(entities []alchemy.Entity) map[string]map[string]any {
	if len(p.absorb) == 0 {
		return nil
	}
	out := map[string]map[string]any{}
	for _, e := range entities {
		into, absorbed := p.absorb[e.ID]
		if !absorbed || len(e.Attributes) == 0 {
			continue
		}
		if out[into] == nil {
			out[into] = map[string]any{}
		}
		for k, v := range e.Attributes {
			if _, taken := out[into][k]; !taken {
				out[into][k] = v
			}
		}
	}
	return out
}

// carry gives a surviving entity what the nodes folded into it stated.
//
// It copies before writing. The attribute map on the input result is shared
// with the caller — Apply promises not to modify what it was given, so that a
// caller holding the pending result while a reviewer works does not watch it
// change underneath them.
func carry(e alchemy.Entity, extra map[string]any) alchemy.Entity {
	if len(extra) == 0 {
		return e
	}
	merged := make(map[string]any, len(e.Attributes)+len(extra))
	for k, v := range e.Attributes {
		merged[k] = v
	}
	for k, v := range extra {
		if _, taken := merged[k]; !taken {
			merged[k] = v
		}
	}
	e.Attributes = merged
	return e
}

// redirect points an edge at the survivors of its two ends, and says whether
// the edge should still exist.
//
// An edge that has become a loop is dropped, and only when the merge is what
// made it one. "document package MENTIONS document" is an edge the model
// proposed because it believed there were two things; a person has just said
// there is one, and a self-loop nothing in any source asserts is not a record —
// it is an artefact of the decision. A loop the source actually stated is kept,
// because that one is a record.
func (p *plan) redirect(r alchemy.Relation) (alchemy.Relation, bool) {
	from, to := p.survivor(r.From), p.survivor(r.To)
	if from == r.From && to == r.To {
		return r, true
	}
	if from == to && r.From != r.To {
		return r, false
	}
	r.From, r.To = from, to
	return r, true
}

// checkMerge refuses the decisions on a duplicate that cannot mean what they
// say, and the Into on a kind that has no use for one.
//
// Reject is the important refusal. On every other kind it removes the record
// the item names; here the item names two nodes, one of which the reviewer
// means to keep, so there is no reading of it that does not delete something
// somebody wanted. What a reviewer means by "these are not the same thing" is
// accept: the graph is left exactly as the extractor produced it, and both
// nodes carry their name, which is how a later reader tells "looked at and kept
// apart" from "nobody looked".
func checkMerge(item Item, d Decision) error {
	if item.Kind != KindDuplicate {
		if d.Edit != nil && d.Edit.Into != "" {
			return fmt.Errorf("review: item %q is a %s and carries a merge into %q; only a duplicate names two nodes that might be one, and merging on any other finding would move records nobody was shown",
				d.ItemID, item.Kind, d.Edit.Into)
		}
		return nil
	}
	if d.Verb == VerbReject {
		return fmt.Errorf("review: item %q asks whether two nodes are one node, which %q cannot answer without deleting one of them; `accept` says they are two, and `edit` with `into` says which one they both are",
			d.ItemID, VerbReject)
	}
	if d.Edit == nil {
		return nil
	}
	if d.Edit.Into == "" {
		return fmt.Errorf("review: item %q was edited without saying which node the two are; a duplicate is answered with `into`, and %+v changes what a record says rather than which record it is",
			d.ItemID, *d.Edit)
	}
	if (Edit{Into: d.Edit.Into}) != *d.Edit {
		// Retyping or renaming as part of a merge would be a second decision
		// travelling on the first, and the finding a reader goes back to says
		// nothing about it.
		return fmt.Errorf("review: item %q merges two nodes and also edits them (%+v); a merge says which node they are and nothing else, so make the correction its own decision",
			d.ItemID, *d.Edit)
	}
	for _, ref := range item.Targets {
		if ref.Kind == RefEntity && ref.ID == d.Edit.Into {
			return nil
		}
	}
	// The survivor has to be one of the two the reviewer was shown. Anything
	// else is a decision about a node that was not in the question, which is
	// how one answer becomes a licence to rewrite the graph — and, when the
	// decision came from a standing rule, a licence nobody is present to
	// notice being used.
	return fmt.Errorf("review: item %q merges into %q, which is not one of the two nodes it asks about; a merge picks one of the pair, it does not name a third node",
		d.ItemID, d.Edit.Into)
}
