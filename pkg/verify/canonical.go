package verify

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// canonicalise rewrites every type to the spelling the ontology declares and
// records each entity's canonical type for the relation checks that follow.
//
// It runs before anything else because both later jobs depend on it. A graph
// that kept Cluster, cluster and CLUSTER carries three node types where the
// ontology declares one, and a traversal keyed on the type name finds a third
// of it; and a conflict check run before folding would report "cluster vs
// Cluster" as two sources disagreeing, which is a person woken up for a
// spelling.
//
// A type the vocabulary does not know is left exactly as the source wrote it.
// Rewriting it would erase the evidence the reviewer needs, and the violation
// pass reports it in the next step.
func canonicalise(in Input, rl *rules) (entities []alchemy.Entity, relations []alchemy.Relation, types map[string]string) {
	entities = make([]alchemy.Entity, 0, len(in.Entities))
	relations = make([]alchemy.Relation, 0, len(in.Relations))
	types = make(map[string]string, len(in.Entities))

	for _, e := range in.Entities {
		if c, ok := rl.canonicalEntity(e.Type); ok {
			e.Type = c
		}
		stamp(&e.Provenance, in.OntologyID)
		// First writer wins, so the type used to check relations is the one a
		// reader of the graph meets first. Where writers disagree the conflict
		// pass reports it; picking a side here would resolve it silently, which
		// §5c forbids.
		if _, seen := types[e.ID]; !seen {
			types[e.ID] = e.Type
		}
		entities = append(entities, e)
	}

	for _, rel := range in.Relations {
		if c, ok := rl.canonicalRelation(rel.Type); ok {
			rel.Type = c
		}
		stamp(&rel.Provenance, in.OntologyID)
		relations = append(relations, rel)
	}
	return entities, relations, types
}

// stamp records which vocabulary this fact was checked against, and only when
// the producer left it blank. §5c: verification adds to provenance, it never
// overwrites it — an entity that already names the ontology it was extracted
// under keeps that name even if this run used another.
func stamp(p *alchemy.Provenance, ontologyID string) {
	if p.Ontology == "" {
		p.Ontology = ontologyID
	}
}
