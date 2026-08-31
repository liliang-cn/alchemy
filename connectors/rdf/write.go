package rdf

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// writeEntities turns one batch of entities into Turtle and posts it.
//
// An entity is a node with a type, a label, its aliases and whatever the source
// said about it. Its provenance is annotated onto its rdf:type statement, which
// is the assertion that brought it into the graph — the same RDF-star shape a
// relation gets, so §5b's "an entity and a relation can both name their
// producer" is one query shape rather than two.
//
// It takes a batch rather than a result because that is what the envelope hands
// it (pkg/sink): §8.4 pages a large graph over the wire precisely because it
// does not fit in one message, and a store that then materialised it to write
// it would have undone the paging.
func (l *Loader) writeEntities(ctx context.Context, graph string, batch []alchemy.Entity) error {
	var d doc
	for _, e := range batch {
		self := iri(l.entityIRI(l.opts.RunID, e.ID))
		class := iri(l.classIRI(e.Type))

		pairs := []pair{
			{iri(rdfType), class},
			// al:Entity is what the anchor search matches on. It is an
			// inclusion list of one: a chunk, a finding and the load marker are
			// left out of Find because they are not entities, rather than
			// because somebody remembered to exclude them.
			{iri(rdfType), iri(clEntity)},
			{iri(pID), lit(e.ID)},
			// The declared type as a literal beside the class IRI. The IRI is
			// percent-escaped and a reader should never have to unescape one to
			// learn what the ontology called this; cortexdb keeps
			// _declared_type for the same reason, one store over.
			{iri(pType), lit(e.Type)},
			{iri(rdfsLabel), lit(e.Name)},
		}
		for _, a := range e.Aliases {
			// skos:altLabel, which is what SKOS is for: other names the source
			// stated for one thing. Several values under one predicate is
			// RDF's native way to say that, and aliases are a set — unlike a
			// JSON array attribute, where writing several triples would
			// silently discard the order. See attrPairs.
			pairs = append(pairs, pair{iri(skosAltLabel), lit(a)})
		}
		attrs, encoded, err := attrPairs(e.Attributes, l)
		if err != nil {
			return fmt.Errorf("rdf: entity %s: %w", e.ID, err)
		}
		pairs = append(pairs, attrs...)
		for _, k := range encoded {
			pairs = append(pairs, pair{iri(pJSONAttribute), lit(k)})
		}
		d.preds(self, pairs...)
		// The class declaration travels with the first entity that uses it
		// rather than being collected up front, because a paged load never sees
		// the whole result: a store that waited to learn every type would have
		// to buffer the graph it exists not to buffer. Re-declaring it in a
		// later batch writes the same triple into the same set, which is
		// nothing.
		d.preds(class,
			pair{iri(rdfType), iri(rdfsClass)},
			pair{iri(rdfType), iri(clEntityType)},
			pair{iri(rdfsLabel), lit(e.Type)})
		d.preds(quoted(self, iri(rdfType), class), provPairs(e.Provenance)...)
	}
	return l.post(ctx, graph, &d)
}

// writeRelations turns one batch of edges into Turtle.
//
// The edge is an ordinary asserted triple, which is what makes this store
// readable by every RDF tool there is: <mira> <rel/DEVELOPS> <ledger> and nothing
// clever. Everything alchemy knows about the assertion — its provenance, the
// producer's own key for it, and whatever the source said about the edge —
// hangs off the quoted form of that same triple.
//
// The caller has already dropped the edges whose endpoints are not in the
// result, and the ones this store cannot keep apart. See tx.go for both.
func (l *Loader) writeRelations(ctx context.Context, graph string, batch []alchemy.Relation) error {
	var d doc
	for _, r := range batch {
		from := iri(l.entityIRI(l.opts.RunID, r.From))
		to := iri(l.entityIRI(l.opts.RunID, r.To))
		pred := iri(l.relIRI(r.Type))

		d.triple(from, pred, to)
		// Declared as a relation type, which is what the one-hop walk matches
		// on. An inclusion list: a predicate is walkable because this connector
		// minted it for an alchemy relation type, so skos:closeMatch, rdf:type,
		// rdfs:label and every al: predicate are outside a walk by
		// construction rather than by an exclusion list somebody has to keep
		// complete.
		d.preds(pred,
			pair{iri(rdfType), iri(rdfProperty)},
			pair{iri(rdfType), iri(clRelationType)},
			pair{iri(rdfsLabel), lit(r.Type)})

		pairs := provPairs(r.Provenance)
		if r.Key != "" {
			// The producer's own name for this edge. It cannot make two
			// parallel edges two edges here — RDF is a set and tx.go reports
			// what that costs — but it is what the producer said and dropping
			// it would lose the only thing that tells one foreign key from the
			// other in a table that references one table twice.
			pairs = append(pairs, pair{iri(pRelationKey), lit(r.Key)})
		}
		attrs, encoded, err := attrPairs(r.Attributes, l)
		if err != nil {
			return fmt.Errorf("rdf: relation %s -[%s]-> %s: %w", r.From, r.Type, r.To, err)
		}
		pairs = append(pairs, attrs...)
		for _, k := range encoded {
			pairs = append(pairs, pair{iri(pJSONAttribute), lit(k)})
		}
		d.preds(quoted(from, pred, to), pairs...)
	}
	return l.post(ctx, graph, &d)
}

// writeChunks writes the text a citation resolves to, and drops the embedding.
//
// A triple store holds no embeddings. The number left behind is
// Report.SkippedVectors, which Load fills from the result — so it says how many
// the job produced rather than how many reached here, which is the number a
// buyer needs in order to go and load them somewhere else.
func (l *Loader) writeChunks(ctx context.Context, graph string, batch []sink.Chunk) error {
	var d doc
	for _, c := range batch {
		pairs := []pair{
			{iri(rdfType), iri(clChunk)},
			// Typed integers, because a citation is resolved by matching this
			// number and ordered by it: as text, chunk 10 falls between 1 and 2.
			{iri(pIndex), intLit(c.Index)},
			{iri(pSource), lit(c.Source)},
			{iri(pText), lit(c.Text)},
			// The byte offsets, which are what make a citation evidence rather
			// than a quotation: a reader holding them can open the file and see
			// the sentence in its place.
			{iri(pStart), intLit(c.Start)},
			{iri(pEnd), intLit(c.End)},
		}
		if c.Strategy != "" {
			pairs = append(pairs, pair{iri(pStrategy), lit(c.Strategy)})
		}
		if c.Heading != "" {
			pairs = append(pairs, pair{iri(pHeading), lit(c.Heading)})
		}
		d.preds(iri(l.chunkIRI(l.opts.RunID, c.Index)), pairs...)
	}
	return l.post(ctx, graph, &d)
}

// post renders a document and sends it, counting the round trip. An empty
// document sends nothing: a batch that produced no statements — every relation
// in it dropped, say — should not cost a request.
func (l *Loader) post(ctx context.Context, graph string, d *doc) error {
	if d.empty() {
		return nil
	}
	body, err := d.render()
	if err != nil {
		return fmt.Errorf("rdf: %w", err)
	}
	l.count()
	return l.addTurtle(ctx, graph, body)
}

// attrPairs turns the model's free-form Attributes into predicate/object pairs,
// and returns the keys whose values had to be re-encoded on the way.
//
// The value domain is the JSON one (alchemy.Entity.Attributes), and RDF holds
// three of its five kinds directly: a string, a number and a boolean are
// literals. An object or an array is written as its JSON text and the key is
// named on the record itself, under al:jsonAttribute, so a buyer reading
// <attr/address> can tell it is JSON rather than a string the source wrote. A
// conversion nobody can see from the data is the kind of quiet rewrite this
// connector exists not to do — neo4j reaches the same answer through its
// json_attrs property, for the same reason.
//
// An array is deliberately NOT written as several triples under one predicate,
// which is the shape RDF makes so easy that it is worth saying why not: a
// JSON array is a sequence and a multi-valued predicate is a set, so three
// triples would silently discard the order. rdf:List preserves it and turns
// every attribute query into a list traversal, which is a price a buyer pays on
// every read for a field most of them never look at. Aliases go the other way —
// several triples — because a set is genuinely what they are.
//
// null is dropped rather than written. RDF has no null; a triple asserting one
// would be asserting that the value is a resource named "null".
func attrPairs(attrs map[string]any, l *Loader) ([]pair, []string, error) {
	if len(attrs) == 0 {
		return nil, nil, nil
	}
	// Sorted, so two loads of one record produce the same document. RDF is a
	// set so the store would not care, but a diff between two runs of a load is
	// something an operator does, and a document that reshuffles itself is one
	// they cannot read.
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []pair
	var encoded []string
	for _, k := range keys {
		t, wasJSON, err := attrValue(attrs[k])
		if err != nil {
			return nil, nil, fmt.Errorf("attribute %q: %w", k, err)
		}
		if t.text == "" && t.err == nil {
			continue // null
		}
		out = append(out, pair{iri(l.attrIRI(k)), t})
		if wasJSON {
			encoded = append(encoded, k)
		}
	}
	return out, encoded, nil
}

// attrValue maps one JSON value onto an RDF term. The second result says
// whether the value had to be re-encoded as JSON text.
func attrValue(v any) (term, bool, error) {
	switch t := v.(type) {
	case nil:
		return term{}, false, nil
	case string:
		return lit(t), false, nil
	case bool:
		return boolLit(t), false, nil
	case float64:
		return floatLit(t), false, nil
	case float32:
		return floatLit(float64(t)), false, nil
	case int:
		return intLit(t), false, nil
	case int32:
		return intLit(int(t)), false, nil
	case int64:
		return intLit(int(t)), false, nil
	}
	// json.Marshal sorts object keys, which matters: a value that re-encoded
	// differently on every load would turn a replay — which RDF makes free —
	// into a second triple beside the first.
	b, err := json.Marshal(v)
	if err != nil {
		return term{}, false, fmt.Errorf("a value of type %T can be neither stored nor encoded: %w", v, err)
	}
	return lit(string(b)), true, nil
}
