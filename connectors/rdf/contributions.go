package rdf

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"

	"github.com/liliang-cn/alchemy/connectors/internal/contributions"
)

// Contributions reports every source that had a hand in one node.
//
// WHAT A TRIPLE STORE CAN SEE. An entity is one IRI, and its provenance is
// annotated onto the one rdf:type statement that brought it into the graph, so
// a node has exactly one source, one chunk and one producer. pkg/preflight
// states the cost in the same words when it reports EntityCorroborated: "a
// store holding one row per entity keeps one of the two provenances, so the
// second source's claim on this node is not recoverable from it".
//
// This store had the strongest reason of the four for that fold to live in the
// envelope rather than here. A graph is a set, so two records under one ID
// would annotate ONE quoted triple twice and put two sources and two producers
// on it with nothing saying which goes with which — worse than losing one, and
// the same defect writeRelations refuses to create for edges, where it keeps
// the first assertion and reports the second in Report.MergedRelations.
// pkg/sink now folds them on the way in, so the second record never reaches
// this writer and the annotation has one of everything by construction.
//
// The other contributions are the edges, and reaching them is this store's own
// shape. An edge is an ordinary asserted triple and everything alchemy knows
// about the assertion hangs off its quoted form, so the provenance of a mention
// is read through << ?s ?p ?o >> exactly as the walk reads it. The predicates
// are an inclusion list — walkable because this connector declared them
// al:RelationType when it wrote the edge — which matters more here than
// anywhere else, because in RDF the annotation triples are themselves triples:
// an unrestricted pattern around this node would count each of its own
// provenance statements as another source having a hand in it.
//
// WHAT IT CANNOT SEE IS THE NAME. A triple names its endpoints by IRI, and the
// IRI is minted from alchemy.Entity.ID; nothing on the edge or its annotation
// records what the asserting document called the node. rdfs:label and
// skos:altLabel are on the entity and belong to the record that created it. So
// Name is filled for the node's own record and empty for every contribution
// recovered from an edge, and the emptiness is the point: joining the label in
// would be one more triple pattern and would report every source as having
// agreed on the node's name, which is exactly the unanimity
// recall.Contributor exists to stop a reader from inferring.
func (l *Loader) Contributions(ctx context.Context, load, id string) (recall.Contributions, error) {
	q, err := l.contributionsSPARQL(load, id)
	if err != nil {
		return recall.Contributions{}, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("rdf: contributions to %q in load %q: %w", id, load, err)
	}
	if len(rows) == 0 {
		return l.nothingContributed(ctx, load)
	}
	// The node's own row goes first, because it is the one that carries a name
	// and the merge keeps the first non-empty Name it sees for a mention. Which
	// row that is cannot be told from the values — an entity may legitimately be
	// called nothing — so it is told from the binding: ?type is projected by the
	// branch that matched the entity and by neither of the edge branches, and
	// writeEntities writes al:type on every entity unconditionally. SPARQL
	// leaves an unmatched branch's variables unbound rather than empty, so the
	// distinction survives the wire.
	var typ string
	own := make([]recall.Contributor, 0, 1)
	edges := make([]recall.Contributor, 0, len(rows))
	for _, r := range rows {
		c := recall.Contributor{
			Source:   r["source"].Value,
			Chunk:    atoi(r["chunk"].Value),
			Producer: alchemy.Producer(r["producer"].Value),
		}
		if _, mine := r["type"]; !mine {
			edges = append(edges, c)
			continue
		}
		c.Name = r["name"].Value
		if len(own) == 0 {
			typ = r["type"].Value
		}
		own = append(own, c)
	}
	return contributions.Assemble(id, typ, append(own, edges...)), nil
}

// contributionsSPARQL is the read. See findSPARQL for why it is a builder.
//
// Three flat branches rather than one pattern with a FILTER, and the shape is
// the same argument claimsSPARQL makes: the entity IRI is bound on both ends of
// an edge and a triple store indexes exactly that, while `?s ?p ?o` with
// FILTER(?s = ent || ?o = ent) is a scan of the whole graph. They are flat
// rather than nested so that each is a pattern a reader can check on its own —
// the node, the edges out, the edges in — and so that a store that plans UNION
// arms independently gets three index lookups.
//
// The node's branch and the edge branches project different variables on
// purpose. ?type and ?name come back bound only for the record that created the
// node, which is what tells the caller which mention may carry a name; SPARQL
// leaves them unbound elsewhere rather than empty, so the distinction survives
// the wire.
func (l *Loader) contributionsSPARQL(load, id string) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	ent := iri(l.entityIRI(load, id))
	if ent.err != nil {
		return "", ent.err
	}
	// The three provenance fields recall.Contributor is made of, and not the
	// whole of provPattern: this answers "who had a hand in this node", and a
	// mention is located by its file, its chunk and its producer. Matching the
	// optional half as well would cost eleven more patterns per branch to fill
	// fields recall.Contributor does not have; a caller who wants an edge's
	// model, confidence or reviewer asks Assertions, which is the method that
	// keeps the whole of a provenance.
	prov := func(subj string) string {
		return fmt.Sprintf("%[1]s <%[2]s> ?source .\n%[1]s <%[3]s> ?chunk .\n%[1]s <%[4]s> ?producer .\n",
			subj, pSource, pChunk, pProducer)
	}
	own := fmt.Sprintf("{ %[1]s <%[2]s> <%[3]s> .\n%[1]s <%[4]s> ?type .\n%[1]s <%[5]s> ?name .\n%[6]s}",
		ent.text, rdfType, clEntity, pType, rdfsLabel,
		prov(fmt.Sprintf("<< %s <%s> ?class >>", ent.text, rdfType)))
	out := fmt.Sprintf("{ %[1]s ?p ?o .\n?p <%[2]s> <%[3]s> .\n%[4]s}",
		ent.text, rdfType, clRelationType, prov(fmt.Sprintf("<< %s ?p ?o >>", ent.text)))
	in := fmt.Sprintf("{ ?s ?p %[1]s .\n?p <%[2]s> <%[3]s> .\n%[4]s}",
		ent.text, rdfType, clRelationType, prov(fmt.Sprintf("<< ?s ?p %s >>", ent.text)))
	return "SELECT DISTINCT ?type ?name ?source ?chunk ?producer WHERE {\n" + scope +
		own + "\nUNION\n" + out + "\nUNION\n" + in + "\n}\n}\nORDER BY ?source ?chunk ?producer", nil
}

// nothingContributed tells the two absences apart, and it is where the
// interface's asymmetry is spent.
//
// An id the load does not hold is an ordinary answer — nothing contributed to a
// node that is not there — and a load that is not here is a caller naming the
// wrong import, which is the bug the load parameter exists for arriving as a
// typo instead of as a silent wrong answer. It costs a request on the empty
// path only, so the ordinary read pays nothing for it.
func (l *Loader) nothingContributed(ctx context.Context, load string) (recall.Contributions, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Contributions{}, err
	}
	if !ok {
		return recall.Contributions{}, fmt.Errorf("%w: %q is not a finished load in this store; "+
			"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
			recall.ErrNoLoad, load)
	}
	return recall.Contributions{}, nil
}
