package verify

import (
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// proposals groups the undeclared types this run met into one entry each.
//
// It is derived from the violations rather than from a second walk of the
// graph, and that is deliberate: the two must never disagree about what is
// undeclared, and a second traversal with its own copy of the rule is how they
// would. What this adds is the shape a person needs to act — the type once
// instead of once per record, the ends it was actually used between, who used
// it, and one record to go and look at.
//
// The ends are observed and nothing more. "MEMBER_OF was used four times,
// always from Person to Team" is a fact about this corpus; whether that is
// what the type MEANS is a judgement, and §2.1 is the argument that a
// plausible judgement nobody made is the failure that survives review. So an
// end whose own type is also undeclared is left out rather than proposed
// alongside — a line proposing two undeclared things at once is a line nobody
// can accept or reject as one thing.
func proposals(violations []alchemy.Violation, entities []alchemy.Entity, rs *rules) []alchemy.Proposal {
	if len(violations) == 0 {
		return nil
	}
	declaredEnds := map[string]string{}
	for _, e := range entities {
		declaredEnds[e.ID] = e.Type
	}
	undeclared := map[string]bool{}
	for _, v := range violations {
		if v.Kind == alchemy.ViolationUnknownEntityType {
			undeclared[v.About.Type] = true
		}
	}

	type acc struct {
		p                     alchemy.Proposal
		from, to, srcs, prods map[string]bool
	}
	byType := map[string]*acc{}
	var order []string

	note := func(kind alchemy.ProposalKind, typ string, ref alchemy.Ref, p alchemy.Provenance) *acc {
		key := string(kind) + "\x00" + typ
		a, seen := byType[key]
		if !seen {
			a = &acc{
				p:    alchemy.Proposal{Kind: kind, Type: typ, Example: ref},
				from: map[string]bool{}, to: map[string]bool{},
				srcs: map[string]bool{}, prods: map[string]bool{},
			}
			byType[key] = a
			order = append(order, key)
		}
		a.p.Records++
		if p.Source != "" {
			a.srcs[p.Source] = true
		}
		if p.Producer != "" {
			a.prods[string(p.Producer)] = true
		}
		return a
	}

	for _, v := range violations {
		switch v.Kind {
		case alchemy.ViolationUnknownEntityType:
			note(alchemy.ProposalEntity, v.About.Type, v.About, v.Provenance)
		case alchemy.ViolationRelationNotAllowed:
			// The type is declared; what is not is the pair it was used
			// between. Proposing it as a new type would be wrong twice — the
			// name is already taken, and the change a person has to weigh is
			// a widening of a rule that already governs records, not an
			// addition that governs none.
			a := note(alchemy.ProposalRelationEnds, v.About.Type, v.About, v.Provenance)
			if declared, ok := rs.declaredEnds(v.About.Type); ok {
				a.p.DeclaredFrom, a.p.DeclaredTo = declared.from, declared.to
			}
			if t, ok := declaredEnds[v.About.From]; ok && !undeclared[t] {
				a.from[t] = true
			}
			if t, ok := declaredEnds[v.About.To]; ok && !undeclared[t] {
				a.to[t] = true
			}
		case alchemy.ViolationUnknownRelationType:
			a := note(alchemy.ProposalRelation, v.About.Type, v.About, v.Provenance)
			// An end whose own type is undeclared, or which names an entity
			// this result does not contain, contributes nothing: the first
			// would propose two things at once and the second is a dangling
			// edge, which is a different violation with a different fix.
			if t, ok := declaredEnds[v.About.From]; ok && !undeclared[t] {
				a.from[t] = true
			}
			if t, ok := declaredEnds[v.About.To]; ok && !undeclared[t] {
				a.to[t] = true
			}
		}
	}

	out := make([]alchemy.Proposal, 0, len(order))
	for _, key := range order {
		a := byType[key]
		a.p.From, a.p.To = keysOf(a.from), keysOf(a.to)
		a.p.Sources = keysOf(a.srcs)
		for _, name := range keysOf(a.prods) {
			a.p.Producers = append(a.p.Producers, alchemy.Producer(name))
		}
		out = append(out, a.p)
	}
	// Entity types first and each list by name, so a person reading two runs of
	// one corpus reads the same document twice. The entity types come first
	// because a relation type's proposal names them: accepting the list top to
	// bottom never leaves a line referring to something further down.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			// Entity types first, then new relation types, then widenings.
			// A list accepted top to bottom never has a line that depends on
			// one further down, and the widenings come last because they are
			// the only ones that change a rule already in force.
			return kindOrder(out[i].Kind) < kindOrder(out[j].Kind)
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func keysOf(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func kindOrder(k alchemy.ProposalKind) int {
	switch k {
	case alchemy.ProposalEntity:
		return 0
	case alchemy.ProposalRelation:
		return 1
	default:
		return 2
	}
}
