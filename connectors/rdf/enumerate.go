package rdf

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Types is the vocabulary of one load.
//
// al:type and not rdf:type, and the difference is the whole of what this store
// had to decide. An entity here carries BOTH: rdf:type al:Entity, which is what
// makes it walkable as one of this connector's records, and al:type "Person",
// which is what the ontology called it. Counting rdf:type would report
// al:Entity once with every entity under it -- a true statement about the
// encoding and no answer at all to what is in the graph.
//
// Grouping inside the named graph rather than over the store, because a corpus
// imported twice is two graphs and their union is not a vocabulary anybody has.
func (l *Loader) Types(ctx context.Context, load string) ([]recall.TypeCount, error) {
	q, err := l.typesSPARQL(load)
	if err != nil {
		return nil, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("rdf: types in load %q: %w", load, err)
	}
	out := make([]recall.TypeCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, recall.TypeCount{Type: r["type"].Value, Count: atoi(r["n"].Value)})
	}
	return out, nil
}

func (l *Loader) typesSPARQL(load string) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"SELECT ?type (COUNT(?e) AS ?n) WHERE {\n%s?e <%s> <%s> ; <%s> ?type .\n}\n}\n"+
			"GROUP BY ?type ORDER BY ?type",
		scope, rdfType, clEntity, pType), nil
}

// OfType reads out one class.
//
// The type is matched as the literal the writer wrote, not folded: a type is
// declared by an ontology, and SPARQL comparing two literals of different case
// as equal is not something this connector should be teaching a caller to rely
// on. Find folds because its input is text somebody typed.
//
// The count and the page are two sub-selects over one pattern, which is the
// shape findSPARQL uses and for the same reason: how many matched is a
// different question from which ones came back, and a store asked them
// separately could answer from two different moments.
func (l *Loader) OfType(ctx context.Context, load, typ string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("rdf: limit = %d is not a number of entities", limit)
	}
	q, err := l.ofTypeSPARQL(load, typ, limit)
	if err != nil {
		return recall.Found{}, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return recall.Found{}, fmt.Errorf("rdf: entities of type %q in load %q: %w", typ, load, err)
	}
	found := recall.Found{Nodes: []recall.Node{}}
	for _, r := range rows {
		found.Total = atoi(r["total"].Value)
		found.Nodes = append(found.Nodes, recall.Node{
			ID: r["id"].Value, Type: r["type"].Value, Name: r["name"].Value,
		})
	}
	return found, nil
}

func (l *Loader) ofTypeSPARQL(load, typ string, limit int) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	want := lit(typ)
	if want.err != nil {
		return "", want.err
	}
	pattern := scope + fmt.Sprintf(
		"?e <%s> <%s> ; <%s> ?id ; <%s> ?type ; <%s> ?name .\nFILTER(?type = %s)\n}\n",
		rdfType, clEntity, pID, pType, rdfsLabel, want.text)
	return fmt.Sprintf(
		"SELECT ?id ?type ?name ?total WHERE {\n"+
			"{ SELECT (COUNT(*) AS ?total) WHERE {\n%[1]s} }\n"+
			"{ SELECT ?id ?type ?name WHERE {\n%[1]s} ORDER BY ?name ?id LIMIT %[2]d }\n}",
		pattern, limit), nil
}
