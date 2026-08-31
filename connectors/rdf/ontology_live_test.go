package rdf

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

const vocabulary = `{
  "id": "sds@3",
  "parts": {
    "prose": {
      "entities": [
        {"name": "Person", "description": "somebody who works on something", "attributes": ["email"]},
        {"name": "Product", "description": "a thing the company ships"}
      ],
      "relations": [
        {"name": "DEVELOPS", "description": "a person works on a product",
         "from": ["Person"], "to": ["Product"], "at_most_one_in": true},
        {"name": "MANAGES", "from": ["Person"], "to": ["Person"], "at_most_one_out": true}
      ]
    }
  }
}`

func loadVocabulary(t *testing.T) *ontology.Ontology {
	t.Helper()
	o, err := ontology.Load(strings.NewReader(vocabulary))
	if err != nil {
		t.Fatalf("ontology.Load: %v", err)
	}
	return o
}

// TestTheOntologysEndsAreNotEmittedAsDomainAndRange is the trap this connector
// is most likely to be "improved" into.
//
// rdfs:domain and rdfs:range are the obvious mapping for a relation type's
// `from` and `to`, and they mean the opposite thing. alchemy's ends are
// constraints — a mismatch is ViolationRelationEndType, which §7.3 delivers
// WITH the graph, so a load genuinely contains records that break them. RDFS's
// are inference licences: a reasoner over such a load would not report the
// violation, it would derive the missing type and add it, inventing a Person
// nobody asserted out of the exact record alchemy flagged.
//
// So this asserts an absence, which is the only way to test a decision not to
// write something.
func TestTheOntologysEndsAreNotEmittedAsDomainAndRange(t *testing.T) {
	l := liveLoader(t, Options{Ontology: loadVocabulary(t)})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := l.loadIRI(l.opts.RunID)
	for _, term := range []string{rdfsNS + "domain", rdfsNS + "range"} {
		rows := l.ask(t, "SELECT ?s ?o WHERE { GRAPH <"+g+"> { ?s <"+term+"> ?o } }")
		if len(rows) != 0 {
			t.Errorf("this load asserts <%s> %d times; alchemy's relation ends are constraints and "+
				"this predicate is an inference licence, so a reasoner would derive the types the "+
				"records were flagged for not having", term, len(rows))
		}
	}
	// And the information is not lost: it is under alchemy's own predicates,
	// where a buyer can check it and no reasoner will conclude from it.
	rows := l.ask(t, "SELECT ?to WHERE { GRAPH <"+g+"> { <"+l.relIRI("DEVELOPS")+"> <"+pDeclaredFrom+"> ?to } }")
	if len(rows) != 1 || rows[0]["to"].Value != l.classIRI("Person") {
		t.Errorf("al:fromType on DEVELOPS = %v, want the Person class", rows)
	}
}

// The cardinality declarations, which are emitted as OWL because that is the
// standard, exact statement of "at most one" — and are safe to emit for a
// reason the ends do not have: a cardinality breach is ConflictCardinality,
// which §7.3 HOLDS, so a result that reaches a store cannot contain the second
// edge that would trigger the entailment. See ontology.go.
func TestCardinalityIsDeclaredInOWLAndAlsoInAlchemysOwnTerms(t *testing.T) {
	l := liveLoader(t, Options{Ontology: loadVocabulary(t)})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := l.loadIRI(l.opts.RunID)
	for _, tc := range []struct{ rel, owl, own string }{
		{"MANAGES", owlFunctional, pAtMostOneOut},
		{"DEVELOPS", owlInverseFn, pAtMostOneIn},
	} {
		if rows := l.ask(t, "SELECT ?x WHERE { GRAPH <"+g+"> { <"+l.relIRI(tc.rel)+"> <"+rdfType+"> ?x . "+
			"FILTER(?x = <"+tc.owl+">) } }"); len(rows) != 1 {
			t.Errorf("%s is not declared <%s>", tc.rel, tc.owl)
		}
		if rows := l.ask(t, "SELECT ?x WHERE { GRAPH <"+g+"> { <"+l.relIRI(tc.rel)+"> <"+tc.own+"> ?x } }"); len(rows) != 1 {
			t.Errorf("%s does not carry <%s>, so the exact claim survives only in the OWL reading", tc.rel, tc.own)
		}
	}
	// BothWays is deliberately not owl:SymmetricProperty; nothing in the
	// fixture declares it, so what is asserted here is that the term never
	// appears at all.
	if rows := l.ask(t, "SELECT ?s WHERE { GRAPH <"+g+"> { ?s <"+rdfType+"> <"+owlNS+"SymmetricProperty> } }"); len(rows) != 0 {
		t.Errorf("this load asserts owl:SymmetricProperty; ontology.BothWays withholds a contradiction "+
			"and licenses no reverse edge, and a symmetric property would license one: %v", rows)
	}
}

// The descriptions, which are the one row of the mapping with no trap in it: a
// comment licenses nothing and every RDF tool shows it.
func TestATypesDescriptionBecomesAComment(t *testing.T) {
	l := liveLoader(t, Options{Ontology: loadVocabulary(t)})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	rows := l.ask(t, "SELECT ?c WHERE { GRAPH <"+l.loadIRI(l.opts.RunID)+"> { <"+
		l.classIRI("Person")+"> <"+rdfsComment+"> ?c } }")
	if len(rows) != 1 || rows[0]["c"].Value != "somebody who works on something" {
		t.Errorf("rdfs:comment on the Person class = %v", rows)
	}
}

// A load with no ontology still gets the classes its data implies, because
// sink.Sink carries a result and not a vocabulary: the ordinary case is a
// caller who has the result and not the document.
func TestALoadWithNoOntologyStillDeclaresTheClassesTheDataUses(t *testing.T) {
	l, load := loaded(t, Options{})
	rows := l.ask(t, "SELECT ?l WHERE { GRAPH <"+l.loadIRI(load)+"> { <"+
		l.classIRI("System")+"> <"+rdfType+"> <"+rdfsClass+"> ; <"+rdfsLabel+"> ?l } }")
	if len(rows) != 1 || rows[0]["l"].Value != "System" {
		t.Errorf("the System class was not declared by the entities that used it: %v", rows)
	}
}
