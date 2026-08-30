package neo4j

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// plan is a whole result checked and ready to write. Nothing reaches the
// database until one of these exists.
//
// It holds indexes into the result rather than copies of it: §8.4's result has
// four hundred thousand records in it, and a connector that doubles a graph in
// memory in order to check it is a connector that dies on the import it was
// bought for.
type plan struct {
	res  alchemy.Result
	opts Options
	// entities and relations are the indexes that will be written, in the
	// order they will be written.
	entities  []int
	relations []int
	// skipped names the relations that will not be written and why, so that
	// "the graph is missing an edge" is never something a buyer has to
	// discover by counting.
	skipped []string
	digest  string
}

// preflight checks everything that can be checked without a database and
// refuses the whole load if anything is wrong.
//
// It is one pass over the result before the first transaction opens because of
// what a mid-load refusal costs. A load that stops at batch nine of twelve
// leaves a partial graph, and while this connector makes a partial graph
// *identifiable* (see the run marker in load.go), identifiable is a
// consolation prize. Anything knowable up front is refused up front.
func preflight(res alchemy.Result, o Options) (*plan, error) {
	o = o.withDefaults()
	if o.RunID == "" {
		return nil, ErrNoRunID
	}

	// §7.3 first, before anything else is even looked at. The service refuses
	// to hand over a held result, but a Result also reaches a connector from a
	// file on disk, from reassembled StreamResult pages, or from a test
	// fixture — and this is the last place before a contradiction becomes a
	// graph an agent will answer from, confidently, with a citation.
	//
	// "Unanswered" is review.Held's definition and not a copy of it. Two
	// definitions of what holds a job is how the guarantee ends: the service
	// would refuse a result the connector would take.
	if open := review.Held(res); len(open) > 0 {
		return nil, fmt.Errorf("%w: %d of %d conflict(s) unanswered, first is %s (%s)",
			ErrHeld, len(open), len(res.Conflicts), open[0].Subject, open[0].Kind)
	}

	p := &plan{res: res, opts: o, digest: digest(res)}

	// The labels this connector keeps for its own bookkeeping. A buyer's
	// ontology type that lands on one of them would make the findings query
	// return entities and the entity query return findings, with nothing about
	// either looking wrong — so it is refused, and the refusal names the knob
	// that frees the name rather than telling the buyer their vocabulary is
	// invalid.
	internal := make(map[string]struct{}, 6)
	for _, l := range o.internalLabels() {
		internal[l] = struct{}{}
	}

	ids := make(map[string]struct{}, len(res.Entities))
	for i, e := range res.Entities {
		if e.ID == "" {
			return nil, fmt.Errorf("entity %d has no ID, so nothing can refer to it", i)
		}
		if _, err := quoteIdent(e.Type); err != nil {
			return nil, fmt.Errorf("entity %s: type cannot be a label: %w", e.ID, err)
		}
		if _, clash := internal[e.Type]; clash {
			return nil, fmt.Errorf("entity %s: type %q is the label this connector writes its own bookkeeping under; "+
				"set Options.BaseLabel to move that namespace", e.ID, e.Type)
		}
		if err := checkAttributes(e.Attributes, e.Name, o.ReservedPrefix); err != nil {
			return nil, fmt.Errorf("entity %s: %w", e.ID, err)
		}
		ids[e.ID] = struct{}{}
		p.entities = append(p.entities, i)
	}

	for i, r := range res.Relations {
		if _, err := quoteIdent(r.Type); err != nil {
			return nil, fmt.Errorf("relation %s->%s: type cannot be a relationship type: %w", r.From, r.To, err)
		}
		// A relation has no name of its own, so "name" is a free attribute
		// here in a way it is not on an entity.
		if err := checkAttributes(r.Attributes, "", o.ReservedPrefix); err != nil {
			return nil, fmt.Errorf("relation %s-[%s]->%s: %w", r.From, r.Type, r.To, err)
		}
		// A dangling relation is ViolationDanglingRelation, and §7.3 puts
		// violations on the "returned, graph delivered" side of the line: one
		// source said something the ontology does not allow, and the rest of
		// the graph is usable without it. So it is skipped rather than fatal.
		// It is not skipped quietly — an edge that disappeared with no record
		// of its disappearance is the silent loss this design refuses.
		_, from := ids[r.From]
		_, to := ids[r.To]
		if !from || !to {
			p.skipped = append(p.skipped, fmt.Sprintf("%s -[%s]-> %s (%s)", r.From, r.Type, r.To, missing(from, to)))
			continue
		}
		p.relations = append(p.relations, i)
	}
	return p, nil
}

func missing(from, to bool) string {
	switch {
	case !from && !to:
		return "neither endpoint is in the result"
	case !from:
		return "the source entity is not in the result"
	default:
		return "the target entity is not in the result"
	}
}

// checkAttributes enforces the one rule that makes the property layout safe:
// everything alchemy knows lives under the reserved prefix, and everything the
// source said lives outside it.
//
// The rule is enforced rather than resolved because both ways of resolving it
// are worse. Letting an attribute win overwrites the provenance §5b promises
// with a field a model chose to call "_producer". Letting alchemy win drops
// something the source actually said, and the buyer's ontology declared it, so
// nothing downstream would report the loss.
//
// name is the one property outside the prefix that alchemy also writes, and it
// is outside because `MATCH (p:Person {name: "SuperAI"})` is the query a Neo4j
// buyer will type. A disagreeing "name" attribute is a collision and is
// refused; an agreeing one is not a collision at all, and failing a large
// import over it would be pedantry with a cost.
func checkAttributes(attrs map[string]any, name, prefix string) error {
	for k, v := range attrs {
		if strings.HasPrefix(k, prefix) {
			return fmt.Errorf("attribute %q is in the reserved %q namespace, where alchemy writes provenance; "+
				"set Options.ReservedPrefix to move the namespace", k, prefix)
		}
		if name != "" && k == "name" {
			if s, ok := v.(string); !ok || s != name {
				return fmt.Errorf("attribute %q is %#v but the entity's name is %q; "+
					"one of the two would have to win silently", k, v, name)
			}
		}
	}
	return nil
}
