package verify

import "github.com/liliang-cn/alchemy/pkg/ontology"

// rules is the vocabulary with its answers remembered for the length of one
// Check.
//
// It exists because the vocabulary matches case-insensitively, which means a
// fold of both sides for every declared type on every lookup — and this stage
// looks each type up several times per record on purpose, so that the check and
// the canonicalisation cannot drift apart. At §8's volumes that is the whole
// budget spent lower-casing the same twelve words a million times.
//
// The vocabulary cannot change while Check runs, so remembering is safe, and
// nothing here reimplements a rule: every answer still comes from the ontology
// package the first time it is asked. The maps are only ever looked up, never
// ranged over, so they cannot leak into the order of the report.
type rules struct {
	vocab      ontology.Vocabulary
	entities   map[string]string
	relations  map[string]string
	endpoints  map[endpointKey]verdict
	ontologyID string
}

type endpointKey struct{ typ, from, to string }

type verdict struct {
	ok     bool
	reason string
}

func newRules(v ontology.Vocabulary, ontologyID string) *rules {
	return &rules{
		vocab:      v,
		entities:   map[string]string{},
		relations:  map[string]string{},
		endpoints:  map[endpointKey]verdict{},
		ontologyID: ontologyID,
	}
}

// canonicalEntity returns the ontology's spelling of an entity type. The empty
// string stands for "not declared", which is unambiguous because Load rejects
// an ontology with an unnamed type.
func (r *rules) canonicalEntity(t string) (string, bool) {
	if c, seen := r.entities[t]; seen {
		return c, c != ""
	}
	c, ok := r.vocab.CanonicalEntity(t)
	if !ok {
		c = ""
	}
	r.entities[t] = c
	return c, ok
}

func (r *rules) canonicalRelation(t string) (string, bool) {
	if c, seen := r.relations[t]; seen {
		return c, c != ""
	}
	c, ok := r.vocab.CanonicalRelation(t)
	if !ok {
		c = ""
	}
	r.relations[t] = c
	return c, ok
}

// allowsRelation answers the endpoint question, keeping the vocabulary's own
// reason. That sentence is written for the person who has to act on it and it
// distinguishes three different jobs — widen the ontology, fix the extraction,
// fix an entity type that is already wrong elsewhere — so it is carried through
// rather than reworded.
func (r *rules) allowsRelation(typ, from, to string) (bool, string) {
	k := endpointKey{typ: typ, from: from, to: to}
	if v, seen := r.endpoints[k]; seen {
		return v.ok, v.reason
	}
	ok, reason := r.vocab.AllowsRelation(typ, from, to)
	r.endpoints[k] = verdict{ok: ok, reason: reason}
	return ok, reason
}

// governs reports whether an ontology is in force for this input.
//
// The question is not "does the vocabulary declare anything" but "was an
// ontology claimed", and the difference decides whether an empty vocabulary is
// silence or a fault:
//
//   - An OntologyID with nothing in it is a broken ontology — it was named, so
//     something was meant to be there. Everything is rejected loudly, which is
//     §5's "there is no unconstrained mode" doing its job: a document import
//     whose vocabulary failed to arrive must not quietly deliver the 74% graph.
//   - Neither an ID nor a vocabulary is a job that never claimed one. §5
//     requires an ontology only for document sources, so a DDL or graph import
//     legitimately arrives with none, and the rule that would have been broken
//     does not exist. (The document-source requirement is enforced upstream in
//     pipeline.validate, before a model is called — this package does not
//     re-decide who owed an ontology.)
//
// It is a question about the input rather than a mode: a caller does not turn
// ontology checking off, and the one place the distinction is read is here.
func (r *rules) governs() bool {
	return r.ontologyID != "" || len(r.vocab.Entities) > 0 || len(r.vocab.Relations) > 0
}
