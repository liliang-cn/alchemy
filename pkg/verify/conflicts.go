package verify

import (
	"fmt"
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// conflicts finds where two claims both assert they are right with nothing in
// the data to decide between them. §7.3: every one of these holds the job,
// whether or not review mode is on, because a graph that contradicts itself is
// worse than no graph — an agent reading it answers from whichever edge it
// happened to traverse, confidently, with a citation.
//
// Source identity is deliberately not part of the test. Two records agreeing is
// corroboration and two records disagreeing is a question, and which files they
// arrived in changes neither. One document that says a thing twice, two ways,
// leaves the same unusable graph as two documents that do; there is no ontology
// rule to point at and no rule for which half to drop, which is exactly what
// makes it a conflict rather than a violation. What provenance is for here is
// the Claim, so the reviewer sees page 4 against page 41.
//
// The scan is keyed by identity, never pairwise: §8.1 names the O(n²) version
// as the plausible-looking implementation that dies at volume. Each record is
// measured against the first claim recorded for its key, so a key carrying a
// thousand records costs a thousand lookups and yields one question per
// distinct disagreement rather than half a million pairs.
func conflicts(entities []alchemy.Entity, relations []alchemy.Relation, rs *rules) []alchemy.Conflict {
	out := entityConflicts(entities)
	out = append(out, relationConflicts(relations, rs)...)
	// Last, and in its own pass, because it is the one question here that is
	// about a node rather than about an edge: see cardinality.go for why that
	// cannot share the edge-keyed scan above.
	return append(out, cardinalityConflicts(relations, rs)...)
}

// slot remembers the first claim made about one key and which later values have
// already been reported against it.
//
// It stores the provenance rather than a finished alchemy.Claim because the
// overwhelmingly common case is a record that agrees with what is already
// there: rendering a sentence for every record in a two-hundred-thousand-record
// job spends the whole budget writing text nobody will read. Statements are
// composed at the moment a conflict is emitted.
type slot struct {
	value string
	prov  alchemy.Provenance
	// others is nil until a second distinct value shows up, which is the normal
	// case, so a clean graph carries one map per group and no more.
	others map[string]bool
}

// disagrees records a later value and reports whether it is news. It is the
// only place a conflict is decided, so "the same statement twice" means one
// thing everywhere: redundancy. Reporting each distinct value once is what
// keeps the queue readable — a hundred records asserting the same wrong type
// are one question, not a hundred.
func (s *slot) disagrees(value string) bool {
	if value == s.value {
		return false // the same statement made twice is corroboration.
	}
	if s.others[value] {
		return false // already asked; asking again is how a queue stops being read.
	}
	if s.others == nil {
		s.others = map[string]bool{}
	}
	s.others[value] = true
	return true
}

type entityGroup struct {
	typ   slot
	attrs map[string]*slot
}

func entityConflicts(entities []alchemy.Entity) []alchemy.Conflict {
	var out []alchemy.Conflict
	groups := make(map[string]*entityGroup, len(entities))

	for _, e := range entities {
		g, seen := groups[e.ID]
		if !seen {
			g = &entityGroup{typ: slot{value: e.Type, prov: e.Provenance}}
			groups[e.ID] = g
		} else if g.typ.disagrees(e.Type) {
			out = append(out, alchemy.Conflict{
				Kind:    alchemy.ConflictEntityType,
				Subject: e.ID,
				Detail: fmt.Sprintf("entity %q is typed %q by %s and %q by %s; the ontology allows both, so only a person can say which this is",
					e.ID, g.typ.value, where(g.typ.prov), e.Type, where(e.Provenance)),
				Left:  claim(typeStatement(e.ID, g.typ.value), g.typ.prov),
				Right: claim(typeStatement(e.ID, e.Type), e.Provenance),
			})
		}

		for _, a := range attributesOf(e) {
			s, ok := g.attrs[a.name]
			if !ok {
				if g.attrs == nil {
					g.attrs = make(map[string]*slot, len(e.Attributes)+1)
				}
				g.attrs[a.name] = &slot{value: a.value, prov: e.Provenance}
				continue
			}
			if !s.disagrees(a.value) {
				continue
			}
			out = append(out, alchemy.Conflict{
				Kind:    alchemy.ConflictEntityAttributes,
				Subject: e.ID + "." + a.name,
				Detail: fmt.Sprintf("entity %q has %s = %s per %s and %s per %s; nothing in the data says which source read it right",
					e.ID, a.name, s.value, where(s.prov), a.value, where(e.Provenance)),
				Left:  claim(attrStatement(e.ID, a.name, s.value), s.prov),
				Right: claim(attrStatement(e.ID, a.name, a.value), e.Provenance),
			})
		}
	}
	return out
}

type attribute struct {
	name  string
	value string
}

func typeStatement(id, typ string) string {
	return fmt.Sprintf("entity %q is of type %q", id, typ)
}

func attrStatement(id, name, value string) string {
	return fmt.Sprintf("entity %q has %s = %s", id, name, value)
}

// attributesOf renders one entity's attributes in a fixed order.
//
// Name travels with them rather than beside them because it is one of them:
// §5c's question — "the same entity from a CSV and a contract PDF, is it one
// customer or two?" — is usually asked by the name first. An attribute nobody
// stated is not a claim that it is empty, so blanks are skipped rather than
// compared against a value someone did state.
func attributesOf(e alchemy.Entity) []attribute {
	if e.Name == "" {
		return sortedAttributes(e.Attributes)
	}
	out := make([]attribute, 0, len(e.Attributes)+1)
	out = append(out, attribute{name: "name", value: e.Name})
	return append(out, sortedAttributes(e.Attributes)...)
}

// sortedAttributes renders a map in a fixed order. Map iteration order is the
// classic way to lose a deterministic report, and a conflict list that reorders
// between two runs of one job is one a reviewer cannot diff.
func sortedAttributes(m map[string]any) []attribute {
	if len(m) == 0 {
		return nil
	}
	out := make([]attribute, 0, len(m))
	for k, v := range m {
		out = append(out, attribute{name: k, value: render(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// render turns an attribute value into something comparable and printable.
// Values arrive from JSON as any, so == would panic on a map or a slice; fmt
// prints maps in sorted key order, which keeps the comparison stable. It also
// makes JSON's 1 and 1.0 the same claim, which they are. Strings take the fast
// path because they are most of what a source states and Sprintf on one is a
// copy for nothing.
func render(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func claim(statement string, p alchemy.Provenance) alchemy.Claim {
	return alchemy.Claim{Statement: statement, Provenance: p}
}

// where names a side of a conflict the way the person deciding it thinks about
// it: the file, and the chunk when there was one.
func where(p alchemy.Provenance) string {
	switch {
	case p.Source == "":
		return fmt.Sprintf("an unnamed %s source", p.Producer)
	case p.Chunk < 0:
		return fmt.Sprintf("%s (%s)", p.Source, p.Producer)
	default:
		return fmt.Sprintf("%s chunk %d (%s)", p.Source, p.Chunk, p.Producer)
	}
}
