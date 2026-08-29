package ontology

import (
	"fmt"
	"strings"
)

// Vocabulary returns the closed list of types declared for one part.
//
// It is the only way into a part, and it takes the part by name on purpose:
// there is no method that returns every type the ontology declares, so a
// caller building a prose extractor cannot reach the code vocabulary without
// having asked for PartCode in as many words. §2.1's third lesson is a
// property of this signature rather than a convention.
//
// An undeclared part is an error, never a zero Vocabulary. Returning an empty
// one would let an extractor run against a vocabulary that constrains nothing,
// which §5 refuses: "Supplying an ontology is required for document sources.
// There is no unconstrained mode."
func (o *Ontology) Vocabulary(p Part) (Vocabulary, error) {
	v, ok := o.parts[p]
	if !ok {
		return Vocabulary{}, fmt.Errorf("ontology %q: declares no %q part (it declares %s)", o.ID, p, joinParts(o.partNames()))
	}
	return v.clone(), nil
}

// clone deep-copies the slices a caller could otherwise append to or overwrite.
// A Vocabulary handed out is a statement of what the ontology allows, and a
// caller that can edit it can widen the rules verification will later apply.
func (v Vocabulary) clone() Vocabulary {
	out := Vocabulary{
		Entities:  make([]EntityType, len(v.Entities)),
		Relations: make([]RelationType, len(v.Relations)),
	}
	for i, e := range v.Entities {
		e.Attributes = append([]string(nil), e.Attributes...)
		out.Entities[i] = e
	}
	for i, r := range v.Relations {
		r.From = append([]string(nil), r.From...)
		r.To = append([]string(nil), r.To...)
		out.Relations[i] = r
	}
	return out
}

func joinParts(parts []Part) string {
	names := make([]string, len(parts))
	for i, p := range parts {
		names[i] = string(p)
	}
	return joinQuoted(names)
}

// AllowsEntity reports whether this part declares the entity type.
func (v Vocabulary) AllowsEntity(t string) bool {
	_, ok := v.CanonicalEntity(t)
	return ok
}

// CanonicalEntity returns the spelling the ontology declares for an entity
// type, matching case-insensitively.
//
// It exists because folding without canonicalising only moves the problem: a
// graph that accepted Cluster, cluster and CLUSTER carries three node types
// where the ontology declares one, and a traversal keyed on the type name
// finds a third of the graph. The verifier normalises to this spelling, so
// what folding buys in tolerance it does not spend in graph consistency.
func (v Vocabulary) CanonicalEntity(t string) (string, bool) {
	for _, e := range v.Entities {
		if fold(e.Name) == fold(t) {
			return e.Name, true
		}
	}
	return "", false
}

// CanonicalRelation returns the spelling the ontology declares for a relation
// type, matching case-insensitively.
func (v Vocabulary) CanonicalRelation(t string) (string, bool) {
	if r, ok := v.relation(t); ok {
		return r.Name, true
	}
	return "", false
}

func (v Vocabulary) relation(t string) (RelationType, bool) {
	for _, r := range v.Relations {
		if fold(r.Name) == fold(t) {
			return r, true
		}
	}
	return RelationType{}, false
}

// AllowsRelation reports whether this part allows relType between fromType and
// toType, and says why not when it does not.
//
// The reason is not a debugging string: it becomes the Detail of an
// alchemy.Violation, which §5c puts at the top of the review queue, so it is
// written for the person who has to act on it. The three failures are three
// different jobs — widen the ontology, fix the extraction, or fix an entity
// type that is already wrong somewhere else — and a single "not allowed" would
// send all three to the same wrong place.
func (v Vocabulary) AllowsRelation(relType, fromType, toType string) (bool, string) {
	r, ok := v.relation(relType)
	if !ok {
		return false, fmt.Sprintf("relation type %q is not declared by this vocabulary; it declares %s",
			relType, joinQuoted(v.relationNames()))
	}
	// An endpoint that is not an entity type at all is reported before the
	// direction, because it is a different fault with a different fix: the
	// entity carrying that type is itself a violation, and saying the relation
	// "runs the wrong way" would point at the edge instead of at its end.
	for _, end := range []struct{ field, typ string }{{"from", fromType}, {"to", toType}} {
		if !v.AllowsEntity(end.typ) {
			return false, fmt.Sprintf("%q is not a declared entity type, so it cannot be the %s end of %q",
				end.typ, end.field, r.Name)
		}
	}
	fromOK := matchesEnd(r.From, fromType)
	toOK := matchesEnd(r.To, toType)
	if fromOK && toOK {
		return true, ""
	}
	return false, fmt.Sprintf("relation type %q is declared, but not from %q to %q; it runs %s -> %s",
		r.Name, fromType, toType, endList(r.From), endList(r.To))
}

// matchesEnd reports whether typ may sit on this end.
//
// An empty end list means "any entity type this part declares", which is why
// the caller checks AllowsEntity first: open is bounded by the part's entity
// vocabulary, never unbounded. Declaring ends is optional per relation because
// a relation that genuinely runs between anything — MENTIONS, RELATES_TO — can
// only be enumerated by copying the entity list, and that copy rots silently
// in the direction that turns valid edges into violations.
func matchesEnd(declared []string, typ string) bool {
	if len(declared) == 0 {
		return true
	}
	for _, d := range declared {
		if fold(d) == fold(typ) {
			return true
		}
	}
	return false
}

// endList renders one end for a person, naming openness rather than printing
// an empty list: "none" would read as "nothing may sit here".
func endList(declared []string) string {
	if len(declared) == 0 {
		return "any declared entity type"
	}
	return strings.Join(declared, "|")
}

func (v Vocabulary) relationNames() []string {
	names := make([]string, len(v.Relations))
	for i, r := range v.Relations {
		names[i] = r.Name
	}
	return names
}
