package cortexdb

import "sort"

// ReservedNodeProperties and ReservedEdgeProperties name what this store
// writes for itself, sorted, for a caller that has to know before a load.
//
// The refusal above is correct and arrives too late to be kind. An ontology
// that declares an attribute called "name" or "description" is an ontology no
// graph can be loaded under, and nothing says so until a graph has been
// extracted and a queue answered — which on a corpus of sixty-seven documents
// was thirty-one minutes of model calls and twenty-eight questions for a
// person, all of it spent on a vocabulary that could never have worked. The
// party holding both the vocabulary and the store can check it the moment the
// vocabulary is written, and these are what it needs to do that.
func ReservedNodeProperties() []string { return sortedKeys(cortexNodeProps) }

// ReservedEdgeProperties is ReservedNodeProperties for a relation's own
// properties.
func ReservedEdgeProperties() []string { return sortedKeys(cortexEdgeProps) }

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
