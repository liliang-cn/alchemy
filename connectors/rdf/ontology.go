package rdf

import (
	"context"
	"fmt"
)

// The vocabulary, written as RDFS and OWL — and the one place in this connector
// where the obvious mapping is the wrong one.
//
// # Why `from` and `to` are not rdfs:domain and rdfs:range
//
// An alchemy relation type declares which entity types may be at each end:
//
//	{"name": "DEVELOPS", "from": ["Person"], "to": ["Product"]}
//
// RDFS has a pair of predicates that look exactly like that:
//
//	ex:DEVELOPS rdfs:domain ex:Person ; rdfs:range ex:Product .
//
// The syntax matches and the semantics are opposite. alchemy's ends are
// CONSTRAINTS: a record asserting DEVELOPS out of something that is not a
// Person breaks the rule, and pkg/verify reports ViolationRelationEndType — a
// refusal a person has to look at. rdfs:domain and rdfs:range are INFERENCE
// LICENCES: they say nothing may be checked and everything may be concluded. A
// reasoner seeing `x DEVELOPS y` and `DEVELOPS rdfs:domain Person` does not
// report a problem — it derives `x rdf:type Person` and adds it to the graph.
//
// So emitting them would take alchemy's refusals and turn them into a
// reasoner's silent type assignments. The exact record that alchemy stopped the
// job over is the one an RDFS reasoner would quietly repair, by inventing a
// type nobody asserted for a node nobody typed, in a store whose whole business
// is what was asserted. Every downstream count of "how many Persons are in this
// graph" would then include them.
//
// The decision is therefore: emit the ends under alchemy's own predicates,
// al:fromType and al:toType, and emit no rdfs:domain or rdfs:range at all. The
// information survives — a buyer can write the SPARQL that checks it, and
// ontology.go's rdfs:comment on the class says in words what the type is for —
// and nothing in the graph licenses a conclusion alchemy did not draw.
//
// Two alternatives were available and both are worse. Emitting domain and range
// "with an explicit note" puts a warning in a comment where a reasoner will
// never read it: the triple is what acts, and a note beside it changes nothing.
// SHACL would state the constraint correctly — sh:targetSubjectsOf and
// sh:class are checks rather than licences — and it is genuinely the right
// long-term answer, but a SHACL shapes graph is a second vocabulary, a second
// serialisation to write, and a validation report to parse, which is a
// connector's worth of work for a check pkg/verify has already run before the
// result ever reached a store.
//
// # Cardinality, where the same trap is taken deliberately
//
// at_most_one_out and at_most_one_in are mapped to owl:FunctionalProperty and
// owl:InverseFunctionalProperty, which the mapping calls for and which is the
// standard, exact statement of "at most one". It is worth being clear that
// these are inference licences too, and what they license: an OWL reasoner
// seeing a functional property with two objects concludes the two objects are
// owl:sameAs each other, and merges them.
//
// That is not what alchemy does. A second edge of an at_most_one_out type is
// ConflictCardinality, which §7.3 refuses — the job stops and a person decides —
// and explicitly not a silent replacement or a merge. So a buyer who runs an
// OWL reasoner over an alchemy load gets a merge where alchemy would have asked
// a question.
//
// It is emitted anyway, for a reason the ends do not have: alchemy holds the
// job on a cardinality conflict, so a result that reaches a store has already
// been checked and cannot contain the second edge that would trigger the
// entailment. The licence is issued over data that cannot exercise it. The ends
// have no such guarantee — ViolationRelationEndType is a violation, which §7.3
// delivers with the graph — so a load can and does contain exactly the records
// rdfs:domain would misread.
//
// al:atMostOneIn and al:atMostOneOut are written beside the OWL terms, so the
// exact claim survives for a reader who does not want the OWL reading.

// writeOntology declares the vocabulary this load was extracted under.
//
// It is a no-op when the caller supplied none, which is the ordinary case:
// sink.Sink carries a result and not an ontology, and a result names its
// vocabulary in every record's provenance without carrying the document. What a
// load without it gets is the classes the data implies — every entity's type is
// declared an rdfs:Class as it is written — and nothing invented.
//
// It runs inside the load's own graph, so dropping the load takes its
// vocabulary with it, and it runs before the first record so that a reader who
// arrives mid-load sees the classes the entities point at.
func (l *Loader) writeOntology(ctx context.Context, graph string) error {
	if l.opts.Ontology == nil {
		return nil
	}
	vocab, err := l.opts.Ontology.Vocabulary(l.opts.OntologyPart)
	if err != nil {
		return fmt.Errorf("rdf: ontology part %q: %w", l.opts.OntologyPart, err)
	}
	var d doc
	// The ontology's own identity, on the load marker, so a reader can ask
	// which vocabulary this graph's classes came from without joining through a
	// record's provenance.
	d.preds(iri(graph), pair{iri(pOntologyID), lit(l.opts.Ontology.ID)})

	for _, e := range vocab.Entities {
		class := iri(l.classIRI(e.Name))
		pairs := []pair{
			{iri(rdfType), iri(rdfsClass)},
			{iri(rdfType), iri(clEntityType)},
			{iri(rdfsLabel), lit(e.Name)},
		}
		if e.Description != "" {
			// rdfs:comment is the standard place for the sentence a person
			// reads to decide whether this is the type they meant. It is the
			// one row of the mapping with no trap in it: a comment licenses
			// nothing and every RDF tool shows it.
			pairs = append(pairs, pair{iri(rdfsComment), lit(e.Description)})
		}
		for _, a := range e.Attributes {
			pairs = append(pairs, pair{iri(pAttribute), lit(a)})
		}
		d.preds(class, pairs...)
	}

	for _, r := range vocab.Relations {
		pred := iri(l.relIRI(r.Name))
		pairs := []pair{
			{iri(rdfType), iri(rdfProperty)},
			{iri(rdfType), iri(clRelationType)},
			{iri(rdfsLabel), lit(r.Name)},
		}
		if r.Description != "" {
			pairs = append(pairs, pair{iri(rdfsComment), lit(r.Description)})
		}
		// The ends, under alchemy's own predicates and never rdfs:domain or
		// rdfs:range. See the file comment: same syntax, opposite semantics.
		for _, from := range r.From {
			pairs = append(pairs, pair{iri(pDeclaredFrom), iri(l.classIRI(from))})
		}
		for _, to := range r.To {
			pairs = append(pairs, pair{iri(pDeclaredTo), iri(l.classIRI(to))})
		}
		if r.AtMostOneOut {
			pairs = append(pairs,
				pair{iri(rdfType), iri(owlFunctional)},
				pair{iri(pAtMostOneOut), boolLit(true)})
		}
		if r.AtMostOneIn {
			pairs = append(pairs,
				pair{iri(rdfType), iri(owlInverseFn)},
				pair{iri(pAtMostOneIn), boolLit(true)})
		}
		if r.BothWays {
			// Deliberately NOT owl:SymmetricProperty. ontology.BothWays says
			// only that neither direction forbids the other, and its own
			// comment refuses the name Symmetric for exactly this reason: a
			// symmetric property entitles a reasoner to write the reverse edge
			// itself, which is an edge no source asserted and no producer can
			// honestly be named for (§5b). alchemy withholds a contradiction;
			// OWL would add a claim.
			pairs = append(pairs, pair{iri(pBothWays), boolLit(true)})
		}
		d.preds(pred, pairs...)
	}
	return l.post(ctx, graph, &d)
}
