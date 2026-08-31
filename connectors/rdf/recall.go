package rdf

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Loader is a recall.Reader.
//
// Nothing in this file mutates the Loader, which is what makes a Reader safe
// for concurrent use — the thing at the other end of it is an agent that will
// ask three of these at once. Report.Requests counts a load's round trips and
// deliberately not a reader's: a read is not part of a load, and a counter
// shared with one would be two goroutines writing an int.
//
// What stays this store's own is what only a triple store has an opinion about:
// that a load is a named graph rather than a property or a view, that a walk is
// an INCLUSION list over predicates rather than an exclusion list over edge
// types, and that an edge's provenance is reached through the quoted form of
// the edge itself.
//
// The inclusion list is the difference worth naming. neo4j excludes its
// bookkeeping relationship types by name and its own comment admits what that
// costs — "a kind missing from here is not an error anywhere" — so a test has to
// hold the list complete or an agent gets handed a duplicate report as a claim
// about the world. Here a predicate is walked because this connector declared it
// an al:RelationType when it wrote the edge. A predicate nobody declared cannot
// be walked, so skos:closeMatch, rdf:type, rdfs:label, every al: predicate and
// every attribute key are outside a walk by construction. That matters more here
// than it would elsewhere, because in RDF the annotation triples are themselves
// triples: `?s ?p ?o` in a graph written by this package matches the
// provenance statements too, with the quoted triple as the subject. Measured on
// the live store — five triples where the data is one edge and four annotations.
var _ recall.Reader = (*Loader)(nil)

// scope is what every read below begins with, and it is the load filter
// pkg/recall makes a parameter rather than an option.
//
// It does two things in one graph pattern, which is the payoff for putting the
// load marker's subject at the load graph's own IRI. GRAPH <g> confines the
// answer to one import — the failure recall's package doc describes, where a
// citation resolved against a different import of the same file and nothing
// about the answer looked wrong. The marker triple confines it to an import
// that finished: a load is written with al:complete false and flipped by
// Commit, so a reader that skipped this would serve a load still arriving as
// though it were whole.
//
// A load that is absent and a load that is unfinished both fail the same
// pattern and both answer nothing, which is what recall specifies for three of
// the four methods. Cite is the one that tells them apart, and it pays for a
// second query to do it.
func (l *Loader) scope(load string) (string, error) {
	g := iri(l.loadIRI(load))
	if g.err != nil {
		return "", fmt.Errorf("rdf: load %q: %w", load, g.err)
	}
	return fmt.Sprintf("GRAPH %[1]s {\n%[1]s <%[2]s> true .\n", g.text, pComplete), nil
}

// Find returns the entities of one load whose name contains name.
func (l *Loader) Find(ctx context.Context, load, name string, limit int) (recall.Found, error) {
	if limit <= 0 {
		// There is no "everything" value, for the reason pgvector's Search
		// refuses k <= 0: an unbounded anchor search over a
		// four-hundred-thousand-record import is a page nobody reads and a
		// query nobody meant.
		return recall.Found{}, fmt.Errorf("rdf: limit = %d is not a number of anchors", limit)
	}
	q, err := l.findSPARQL(load, name, limit)
	if err != nil {
		return recall.Found{}, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return recall.Found{}, fmt.Errorf("rdf: find %q in load %q: %w", name, load, err)
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

// findSPARQL is the anchor query. The four builders in this file are separate
// from the calls that run them so that the invariant every one of them shares —
// that a read is scoped to one load, and to a finished one — is assertable
// without a server. A query is otherwise only ever tested on the machines that
// have one.
//
// The count and the page come from one request, over one pattern spelled once
// and used by both subselects. Two requests would count a store that had moved
// between them; two copies of the pattern would be the four-lists problem in a
// third form. What the count answers is "how many matched", not "how many came
// back": a page that does not say it is a page asks a reader to trust a list
// that is not the list, and an agent handed a silently truncated one invents an
// id rather than reporting the truncation.
//
// The search is a substring match, case-insensitively, and not a similarity
// search: this is how a question enters the graph and an agent that asked for
// "Ravel" and got the five nearest names could not tell an exact hit from a
// neighbour. LCASE is applied to the stored name and the search text is
// lowered in Go, so only one of the two is folded per row.
func (l *Loader) findSPARQL(load, name string, limit int) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	needle := lit(strings.ToLower(name))
	if needle.err != nil {
		return "", needle.err
	}
	pattern := scope + fmt.Sprintf(
		"?e <%s> <%s> ; <%s> ?id ; <%s> ?type ; <%s> ?name .\n"+
			"FILTER(CONTAINS(LCASE(?name), %s))\n}\n",
		rdfType, clEntity, pID, pType, rdfsLabel, needle.text)
	// Ordered by name then id, so a limit cuts the same place twice: a pack
	// built twice from one unchanged load must come out the same, or an
	// agent's cache and a diff between two runs are comparing shuffles.
	return fmt.Sprintf(
		"SELECT ?id ?type ?name ?total WHERE {\n"+
			"{ SELECT (COUNT(*) AS ?total) WHERE {\n%[1]s} }\n"+
			"{ SELECT ?id ?type ?name WHERE {\n%[1]s} ORDER BY ?name ?id LIMIT %[2]d }\n}",
		pattern, limit), nil
}

// Assertion is one claim together with the whole provenance the store carries
// for it, rather than the four fields recall.Claim reads.
//
// It is exported because it is the evidence for this connector's central
// decision. RDF-star was chosen over reification so that every one of
// alchemy.Provenance's fields could travel on the assertion itself; a buyer who
// cannot get them back has bought the argument and not the thing. recall.Claim
// is deliberately narrower — it is what a context pack needs — and Claims below
// projects onto it.
type Assertion struct {
	recall.Claim
	// Provenance is the edge's own, unabridged: the model, the ontology, the
	// confidence, the reviewer, the rule set, the rule that acted, the person
	// and the date, as well as the three recall.Claim carries.
	Provenance alchemy.Provenance
}

// Claims returns every claim adjacent to one entity, in either direction, each
// carrying the provenance of the edge rather than of its subject.
//
// Of the edge, which is the correction that matters. A node and an edge both
// carry a full provenance here — deliberately, so §5b's guarantee is one query
// shape — so a walk that read the subject's would return plausible values on
// every row and attribute every claim about an entity to whatever sentence
// first named it.
func (l *Loader) Claims(ctx context.Context, load, id string) ([]recall.Claim, error) {
	all, err := l.Assertions(ctx, load, id)
	if err != nil {
		return nil, err
	}
	out := make([]recall.Claim, 0, len(all))
	for _, a := range all {
		out = append(out, a.Claim)
	}
	return out, nil
}

// Assertions is Claims with the whole provenance kept.
func (l *Loader) Assertions(ctx context.Context, load, id string) ([]Assertion, error) {
	q, err := l.claimsSPARQL(load, id)
	if err != nil {
		return nil, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("rdf: claims about %q in load %q: %w", id, load, err)
	}
	out := make([]Assertion, 0, len(rows))
	for _, r := range rows {
		p := provDecode(r)
		// Through recall.NewClaim, so that stated-or-inferred is
		// alchemy.Producer.Deterministic and not the al:stated triple sitting
		// beside the producer on the same quoted triple. That triple is
		// written for the buyer's own SPARQL, and it is the rule as it stood on
		// the day of the import; a reader deciding how far to trust a sentence
		// today should be told today's answer.
		out = append(out, Assertion{
			Claim: recall.NewClaim(
				recall.Endpoint{ID: l.entityIDFromIRI(load, r["s"].Value), Name: r["subject"].Value},
				recall.Endpoint{ID: l.entityIDFromIRI(load, r["o"].Value), Name: r["object"].Value},
				r["rel"].Value, p),
			Provenance: p,
		})
	}
	return out, nil
}

// claimsSPARQL is the one-hop walk. See findSPARQL for why it is a builder.
//
// The two directions are two index lookups joined by UNION rather than one scan
// with a FILTER, because the entity IRI is bound on both sides and a triple
// store indexes exactly that. An agent asking what is known about a thing does
// not care which way the extractor happened to write the edge — the same
// argument pgvector's Around makes for the same choice — but the direction is
// still reported as the extractor wrote it, because a claim read back pointing
// the other way is a different claim.
//
// DISTINCT because a self-loop matches both arms of the UNION, and because two
// annotations that agree on every projected field render as the same sentence;
// a pack printing one twice would tell a reader the corpus said it twice.
func (l *Loader) claimsSPARQL(load, id string) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	ent := iri(l.entityIRI(load, id))
	if ent.err != nil {
		return "", ent.err
	}
	const q = "<< ?s ?p ?o >>"
	// ?s and ?o are projected beside the labels, because a triple names its
	// ends by IRI and the ID a walk needs is inside it. They are the same two
	// terms the labels are read off, in the same row, so a name cannot be
	// paired with another node's identifier.
	return "SELECT DISTINCT ?s ?o ?subject ?rel ?object" + provVars() + " WHERE {\n" + scope +
		fmt.Sprintf(
			// The inclusion list: ?p is walkable because this connector
			// declared it a relation type when it wrote the edge.
			"?p <%[1]s> <%[2]s> ; <%[3]s> ?rel .\n"+
				"{ %[4]s ?p ?o . BIND(%[4]s AS ?s) } UNION { ?s ?p %[4]s . BIND(%[4]s AS ?o) }\n"+
				// Names on both ends, because a claim is read by a person or a
				// model and "e17 -[USES]-> e04" is not a claim anybody can
				// weigh. The ID stays reachable through Find, which is where an
				// identifier is the thing being asked for.
				"?s <%[5]s> ?subject .\n?o <%[5]s> ?object .\n%[6]s}\n",
			rdfType, clRelationType, rdfsLabel, ent.text, rdfsLabel, provPattern(q)) +
		"}\nORDER BY ?rel ?object ?subject ?source ?chunk", nil
}

// Cite resolves one [source#index] marker against one load.
//
// Both halves have to match. Matching the index alone would work — a job's
// chunk indexes are unique across the whole job, so within a load the number
// identifies the chunk — and it is the wrong shape anyway: the marker a reader
// holds says a file and a number, and a caller who passed the right number with
// the wrong file would be handed the other file's text with nothing about the
// answer looking wrong.
//
// Three outcomes, not two, and the third is the common one. ErrNoChunk when
// the marker carries no chunk number, which is an ordinary answer: the producer
// did not work in chunks and there was never any text under this claim.
// ErrNoCitation when the load holds no such chunk, which IS a failure — a claim
// pointing at material that was not loaded. ErrNoLoad when there is no finished
// load of that name, which is a caller naming the wrong import, the bug the
// load parameter exists for arriving as a typo instead of as a wrong answer.
// Never a zero Citation for any of the three.
//
// The first two were one error until a measurement separated them: across
// thirty runs of an agent over a graph loaded here, seven of thirteen citation
// attempts were against a graph-import source whose chunk is -1, and every one
// was refused with the sentence reserved for evidence that does not check out.
// All seven were false alarms — §5b ranks a machine reading something that
// already asserted a fact ABOVE a model reading prose — and the agents cited
// the claims regardless, which is a tool teaching its caller to ignore it.
func (l *Loader) Cite(ctx context.Context, load, source string, index int) (recall.Citation, error) {
	// A negative index is not a lookup that failed, it is a marker with no chunk
	// number in it, and there is nothing to ask the store for. It goes through
	// whyNoCitation anyway, because the load is checked before anything else:
	// answering "this claim has no text, and that is fine" for an import that is
	// not here would be an ordinary answer handed back for a caller's mistake,
	// which is the one thing the load parameter exists to prevent.
	if index < 0 {
		return recall.Citation{}, l.whyNoCitation(ctx, load, source, index)
	}
	q, err := l.citeSPARQL(load, source, index)
	if err != nil {
		return recall.Citation{}, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return recall.Citation{}, fmt.Errorf("rdf: cite %s#%d in load %q: %w", source, index, load, err)
	}
	if len(rows) == 0 {
		return recall.Citation{}, l.whyNoCitation(ctx, load, source, index)
	}
	r := rows[0]
	return recall.Citation{
		Source: r["source"].Value, Index: atoi(r["index"].Value),
		Start: atoi(r["start"].Value), End: atoi(r["end"].Value), Text: r["text"].Value,
	}, nil
}

// citeSPARQL resolves a chunk. See findSPARQL for why it is a builder.
func (l *Loader) citeSPARQL(load, source string, index int) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	src := lit(source)
	if src.err != nil {
		return "", src.err
	}
	// The index is compared as the integer it was written as. Wrapping it in
	// STR() to compare against text — which is what a hand-written query does
	// when the agent passes a string — turns every chunk in the store into a
	// string conversion and makes chunk 10 compare as though it were between 1
	// and 2.
	return "SELECT ?source ?index ?start ?end ?text WHERE {\n" + scope +
		fmt.Sprintf("?c <%s> <%s> ; <%s> ?index ; <%s> ?source ; <%s> ?text ; <%s> ?start ; <%s> ?end .\n"+
			"FILTER(?index = %d && ?source = %s)\n}\n} LIMIT 1",
			rdfType, clChunk, pIndex, pSource, pText, pStart, pEnd, index, src.text), nil
}

// whyNoCitation tells the two absences apart. It runs only when a citation
// failed, so the ordinary path pays nothing for it.
func (l *Loader) whyNoCitation(ctx context.Context, load, source string, index int) error {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %q is not a finished load in this store; "+
			"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
			recall.ErrNoLoad, load)
	}
	// The claim never had a chunk, which Mark already says by rendering its
	// marker as [source] with no #n. It is an ordinary answer and deliberately
	// not ErrNoCitation: the two say opposite things about how far to trust the
	// claim, and conflating them refused every graph-import claim in the store
	// with the sentence reserved for evidence that does not check out.
	if index < 0 {
		return fmt.Errorf("%w: the claim citing %q carries no chunk number, so load %q holds no text "+
			"to quote for it — the claim is not weakened by that, and must not be reported as uncited",
			recall.ErrNoChunk, source, load)
	}

	return fmt.Errorf("%w: load %q holds no chunk %d of %q — the claim that cited it cannot be checked "+
		"against this import, and must not be offered as evidence from it",
		recall.ErrNoCitation, load, index, source)
}

// Unanswered returns the identity questions this load carries.
//
// They are the duplicate findings, which this connector deliberately does not
// turn into a claim: the skos:closeMatch it writes beside them is the weakest
// term RDF has for "these may be the same" and no walk follows it. Keeping the
// question off the walk only pays if there is a way to ask it, and this is it —
// otherwise the honest reading of a graph loaded here would be that nothing is
// in doubt.
//
// An empty about returns all of them, rather than a sentinel word: "all" is a
// plausible name for a table, a column or a system, and a filter that stops
// filtering for one legal input is worse than no filter.
//
// A load written with Options.SkipFindings holds no duplicates and answers
// nothing here. The load marker carries al:countDuplicates, which is what the
// job found rather than what was written, so a caller can tell "nothing is in
// doubt" from "the doubts were not imported".
func (l *Loader) Unanswered(ctx context.Context, load, about string) ([]recall.Question, error) {
	q, err := l.unansweredSPARQL(load, about)
	if err != nil {
		return nil, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("rdf: unanswered questions about %q in load %q: %w", about, load, err)
	}
	out := make([]recall.Question, 0, len(rows))
	for _, r := range rows {
		out = append(out, recall.Question{
			Signal:  alchemy.DuplicateSignal(r["signal"].Value),
			Subject: r["subject"].Value, Detail: r["detail"].Value,
			Left: r["left"].Value, Right: r["right"].Value,
		})
	}
	return out, nil
}

// unansweredSPARQL reads the identity questions. See findSPARQL for why it is a
// builder.
//
// It searches every field a person would recognise the pair by rather than the
// detail alone: alchemy renders the pair into Subject, states the case in
// Detail, and keeps each side's name separately, so "touching a subject" is
// four values and a query that searched one of them would miss the pair a
// reader was asking about by name.
func (l *Loader) unansweredSPARQL(load, about string) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("?d <%s> <%s> ; <%s> ?signal ; <%s> ?subject ; <%s> ?detail ; <%s> ?left ; <%s> ?right .\n",
		rdfType, clDuplicate, pSignal, pSubject, pDetail, pLeftName, pRightName)
	if about != "" {
		needle := lit(strings.ToLower(about))
		if needle.err != nil {
			return "", needle.err
		}
		var clauses []string
		for _, v := range []string{"?subject", "?detail", "?left", "?right"} {
			clauses = append(clauses, fmt.Sprintf("CONTAINS(LCASE(%s), %s)", v, needle.text))
		}
		body += "FILTER(" + strings.Join(clauses, " || ") + ")\n"
	}
	return "SELECT ?signal ?subject ?detail ?left ?right WHERE {\n" + scope + body +
		"}\n}\nORDER BY ?subject ?detail", nil
}
