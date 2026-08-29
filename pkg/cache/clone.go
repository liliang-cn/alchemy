package cache

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// clone copies an entry deeply enough that nothing the caller holds can reach
// the stored one, and nothing the store holds can reach the caller's.
//
// It is applied on both Put and Get, not one of them. Cloning only on Put still
// hands every reader the same backing array, so the first caller to edit what
// it was given rewrites what the next caller receives; cloning only on Get
// leaves the writer holding the store's array. A cache is meant to make a
// resumed job identical to a fresh one (§8.2), and an aliased entry makes it
// silently different — same counts, same provenance fields, different graph.
//
// The depth stops where alchemy's types stop being ours. Entities and relations
// are copied element by element, and Attributes — the one reference field
// inside them — gets a fresh map. Attribute values are `any`: a caller that
// puts a mutable value inside one can still reach it, and a deep-copy of
// arbitrary `any` would need reflection to do badly what the extractor can
// avoid by storing scalars, which is what it does.
func clone(e Entry) Entry {
	out := Entry{Tokens: e.Tokens}
	if e.Entities != nil {
		out.Entities = make([]alchemy.Entity, len(e.Entities))
		for i, ent := range e.Entities {
			ent.Attributes = cloneAttrs(ent.Attributes)
			out.Entities[i] = ent
		}
	}
	if e.Relations != nil {
		out.Relations = make([]alchemy.Relation, len(e.Relations))
		for i, rel := range e.Relations {
			rel.Attributes = cloneAttrs(rel.Attributes)
			out.Relations[i] = rel
		}
	}
	return out
}

// cloneAttrs preserves nil as nil: an entity that stated no attributes should
// come back stating no attributes, not an empty map, because the two serialise
// differently and the JSON is the contract (§4).
func cloneAttrs(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
