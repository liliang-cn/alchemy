package verify

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// violations checks every entity and relation against the vocabulary.
//
// Nothing here holds a job (§7.3). Each finding is one source saying something
// the ontology does not allow: it names an item, it names why, and the graph
// minus that item is still usable — which is exactly what makes it safe to
// return and let the caller decide.
//
// The walk is in input order, entities before relations, so the output order is
// a property of the input rather than of a map.
func violations(entities []alchemy.Entity, relations []alchemy.Relation, types map[string]string, rs *rules) []alchemy.Violation {
	var out []alchemy.Violation

	// An absent vocabulary means there are no ontology rules, not that every
	// type is undeclared. §5 requires an ontology only for document sources, so
	// a DDL or graph import legitimately arrives with none — and reporting one
	// violation per table, each saying an ontology nobody supplied disallows
	// what a CREATE TABLE stated, would fill §5's obligation to report the
	// numbers needed to distrust a graph with a number that means nothing.
	//
	// It narrows the walk rather than skipping it: dangling ends are structural,
	// not ontological, and an edge naming an entity the result does not contain
	// corrupts every walker whatever vocabulary is in force.
	governed := rs.governs()

	for _, e := range entities {
		if !governed {
			break
		}
		if _, ok := rs.canonicalEntity(e.Type); ok {
			continue
		}
		out = append(out, alchemy.Violation{
			Kind:       alchemy.ViolationUnknownEntityType,
			Subject:    e.ID,
			Detail:     fmt.Sprintf("entity type %q is not declared by %s; it declares %s", e.Type, describe(rs.ontologyID), quoted(entityNames(rs.vocab))),
			Provenance: e.Provenance,
		})
	}

	// edge() is rendered inside each branch below rather than once per relation:
	// a clean job takes none of them, and a string built for a message nobody
	// sends is the sort of cost §8 notices.
	for _, r := range relations {
		_, knownType := rs.canonicalRelation(r.Type)

		// An undeclared relation type is reported on its own terms rather than
		// through AllowsRelation, because it is true regardless of the ends: the
		// fix is to widen the ontology or retype the edge, and naming endpoints
		// that were never consulted would point at the wrong thing.
		if !knownType && governed {
			out = append(out, alchemy.Violation{
				Kind:       alchemy.ViolationUnknownRelationType,
				Subject:    edge(r),
				Detail:     fmt.Sprintf("relation type %q is not declared by %s; it declares %s", r.Type, describe(rs.ontologyID), quoted(relationNames(rs.vocab))),
				Provenance: r.Provenance,
			})
		}

		// Dangling is checked next and short-circuits the endpoint rule. An edge
		// whose end is not in the result has no type to check, so asking whether
		// the relation is allowed between those types would invent an answer.
		if missing := missingEnds(r, types); len(missing) > 0 {
			out = append(out, alchemy.Violation{
				Kind:       alchemy.ViolationDanglingRelation,
				Subject:    edge(r),
				Detail:     fmt.Sprintf("%s names %s, which this result does not contain", edge(r), strings.Join(missing, " and ")),
				Provenance: r.Provenance,
			})
			continue
		}

		if !governed || !knownType {
			continue // ungoverned, or already reported; either way there is no
			// endpoint rule left to consult.
		}
		if ok, reason := rs.allowsRelation(r.Type, types[r.From], types[r.To]); !ok {
			out = append(out, alchemy.Violation{
				Kind:       alchemy.ViolationRelationNotAllowed,
				Subject:    edge(r),
				Detail:     reason,
				Provenance: r.Provenance,
			})
		}
	}

	return out
}

// edge renders a relation the way §7.3's reviewer reads it, and is the Subject
// of every relation violation so that two findings about one edge sort together
// in a queue.
func edge(r alchemy.Relation) string {
	return fmt.Sprintf("%s -[%s]-> %s", r.From, r.Type, r.To)
}

// missingEnds returns the endpoint IDs the result does not contain, in from-to
// order so the message reads the way the edge does.
func missingEnds(r alchemy.Relation, types map[string]string) []string {
	var missing []string
	for _, id := range []string{r.From, r.To} {
		if _, ok := types[id]; !ok {
			missing = append(missing, fmt.Sprintf("%q", id))
		}
	}
	return missing
}

func relationNames(v ontology.Vocabulary) []string {
	names := make([]string, len(v.Relations))
	for i, r := range v.Relations {
		names[i] = r.Name
	}
	return names
}

// describe names the vocabulary a reviewer has to go and change. The ID is
// optional in Input because a caller checking an ad-hoc vocabulary has none,
// and "the ontology" is still a true sentence when it is missing.
func describe(ontologyID string) string {
	if ontologyID == "" {
		return "the ontology"
	}
	return fmt.Sprintf("ontology %q", ontologyID)
}

func entityNames(v ontology.Vocabulary) []string {
	names := make([]string, len(v.Entities))
	for i, e := range v.Entities {
		names[i] = e.Name
	}
	return names
}

func quoted(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}
