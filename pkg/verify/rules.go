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
	oneWay     map[string]bool
	atMostOne  map[string]cardinality
	endpoints  map[endpointKey]verdict
	ontologyID string
}

// cardinality is what one relation type says about how many edges of it may
// meet at one node, at each end.
type cardinality struct{ in, out bool }

func (c cardinality) constrains() bool { return c.in || c.out }

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
		oneWay:     map[string]bool{},
		atMostOne:  map[string]cardinality{},
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

// runsOneWay reports whether an ontology declared this relation type to run in
// one direction only — the question the direction checks have to ask before
// they report two records running opposite ways.
//
// It is the same question governs() asks one field over. Reporting a direction
// conflict is asserting that the type is asymmetric, and nothing but an
// ontology has ever been entitled to say what a relation type means; a type
// nobody declared has no such claim attached, so there is no rule for two
// ordinary facts to have broken. Over one customer's real code graph the
// difference was 79 held questions with no right answer — two Java classes that
// each import the other, recorded correctly, on a job §7.3 will not let finish.
//
// It deliberately does not consult governs(). governs() decides whether a
// vocabulary is in force at all; this decides what one type in it says, and a
// job whose ontology failed to arrive is one where nothing says anything —
// which is the same answer for the same reason, reached without borrowing a
// different rule's judgement.
func (r *rules) runsOneWay(typ string) bool {
	if v, seen := r.oneWay[typ]; seen {
		return v
	}
	v := r.vocab.RunsOneWay(typ)
	r.oneWay[typ] = v
	return v
}

// holdsAtMostOne answers what a relation type declared about how many of its
// edges may meet at one node, at each end.
//
// It is asked through canonicalRelation rather than by folding the name here,
// so that a type is matched exactly once in this package and the cardinality
// check cannot start recognising a spelling the canonicalisation does not. An
// undeclared type answers "neither end", which is the same answer runsOneWay
// gives it and for the same reason one field over.
//
// It reads the declaration off the vocabulary rather than asking the ontology
// package a question, because the ontology package has no question to ask yet:
// AtMostOneIn and AtMostOneOut are exported fields with no accessor beside
// RunsOneWay's. That is a gap worth closing there rather than here — a second
// caller would want the same lookup — and the matching, which is the part that
// could actually drift, is still done by the ontology.
func (r *rules) holdsAtMostOne(typ string) cardinality {
	if c, seen := r.atMostOne[typ]; seen {
		return c
	}
	// Asked of the vocabulary rather than read off Vocabulary.Relations here.
	// The matching is the part that drifts — a type named in one spelling and
	// declared in another has to resolve the same way for every caller — and
	// this file's first version looped over the declarations itself, which is
	// the second copy of a rule pkg/ontology owns. HoldsAtMostOneIn answers
	// false for a type the ontology does not declare, which is what makes an
	// undeclared type a violation and never also a cardinality conflict.
	c := cardinality{
		in:  r.vocab.HoldsAtMostOneIn(typ),
		out: r.vocab.HoldsAtMostOneOut(typ),
	}
	r.atMostOne[typ] = c
	return c
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

// ends are the pair of end lists an ontology declares for a relation type.
type ends struct{ from, to []string }

// declaredEnds reports what the vocabulary already says a relation type runs
// between, so a widening proposal can be read as a diff rather than as a list
// somebody has to go and compare by hand.
//
// It goes through canonicalRelation for the reason every other lookup here
// does: a type named in one spelling and declared in another has to resolve
// the same way, and a second matching loop is the copy that drifts.
func (r *rules) declaredEnds(typ string) (ends, bool) {
	name, ok := r.canonicalRelation(typ)
	if !ok {
		return ends{}, false
	}
	for _, declared := range r.vocab.Relations {
		if declared.Name == name {
			return ends{from: declared.From, to: declared.To}, true
		}
	}
	return ends{}, false
}
