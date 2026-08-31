package rdf

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// The findings are written beside the graph rather than into it, and the
// distinction is what this file is about.
//
// A violation, a duplicate, a guess and an unread page are not claims about the
// world. They are records of what went wrong producing the claims, and §5's
// obligation is that "every returned graph is accompanied by the numbers needed
// to distrust it". So each one is a node of its own, in the load's graph, with
// its own class — and the one-hop walk cannot reach any of them, because a walk
// follows predicates this connector declared to be alchemy relation types and
// none of these are.
//
// Provenance goes directly on the finding node, where an entity's and a
// relation's are annotated onto the triple that asserts them. That is not an
// inconsistency: a finding IS a record rather than an assertion, so the
// provenance describes the node itself and there is no triple to annotate.

// writeFindings writes everything sink.Findings carries. It arrives whole
// rather than in batches, because findings are bounded by what went wrong
// where records are bounded by the corpus.
func (l *Loader) writeFindings(ctx context.Context, graph string, f sink.Findings, rep *Report) error {
	var d doc
	for i, v := range f.Violations {
		pairs := []pair{
			{iri(rdfType), iri(clViolation)},
			{iri(pKind), lit(string(v.Kind))},
			{iri(pSubject), lit(v.Subject)},
			{iri(pDetail), lit(v.Detail)},
		}
		// The record it is about, as the record itself where this load holds
		// one. See refTerm: a violation about an edge points at the quoted
		// triple, which is the same term its provenance is annotated onto.
		if t, ok := l.refTerm(v.About); ok {
			pairs = append(pairs, pair{iri(pAbout), t})
		}
		pairs = append(pairs, provPairs(v.Provenance)...)
		d.preds(iri(l.recordIRI(l.opts.RunID, "violation", i)), pairs...)
	}

	for i, dup := range f.Duplicates {
		self := iri(l.recordIRI(l.opts.RunID, "duplicate", i))
		left := iri(l.entityIRI(l.opts.RunID, dup.Left.ID))
		right := iri(l.entityIRI(l.opts.RunID, dup.Right.ID))
		d.preds(self,
			pair{iri(rdfType), iri(clDuplicate)},
			pair{iri(pSignal), lit(string(dup.Signal))},
			pair{iri(pSubject), lit(dup.Subject)},
			pair{iri(pDetail), lit(dup.Detail)},
			// The names, because recall.Question is names and not IDs: both
			// stores hold them on the finding itself, and a field one store
			// fills from the finding and the other from a second traversal is
			// a field whose cost a caller cannot see.
			pair{iri(pLeftName), lit(dup.Left.Name)},
			pair{iri(pRightName), lit(dup.Right.Name)},
			pair{iri(pLeft), left},
			pair{iri(pRight), right},
		)
		// skos:closeMatch, and NEVER owl:sameAs.
		//
		// This is the single most consequential line in the mapping. A
		// duplicate finding is a QUESTION nobody has answered — alchemy
		// declines to merge on it, and recall.Unanswered exists so an agent can
		// say "these two may be one thing and nobody has decided" instead of
		// answering as though somebody had. owl:sameAs is an assertion of
		// identity: a reasoner given one is entitled to merge the two nodes,
		// rewrite every triple about either onto both, and produce a graph in
		// which the question has been answered — on evidence alchemy explicitly
		// refuses to act on, by a component nobody asked.
		//
		// skos:closeMatch is the term that says what the finding says. SKOS
		// defines it as a link between concepts that "may be used
		// interchangeably in some information retrieval applications", it is
		// deliberately not transitive, and no standard entailment regime draws
		// any conclusion from it. It puts the guess in the graph in the
		// vocabulary the world already has for guesses, and licenses nothing.
		//
		// Even so, the one-hop walk does not follow it — a predicate is walked
		// because this connector minted it for an alchemy relation type, and
		// this is not one — so an agent building a context pack is never handed
		// the guess in the same struct as a claim.
		d.triple(left, iri(skosCloseMatch), right)
		// Annotated with which check fired, so the guess in the graph carries
		// its own strength. A name-affix match and an identical-attribute match
		// are not equally good evidence, and a reader who found the closeMatch
		// without the finding would otherwise have no way to tell them apart.
		d.preds(quoted(left, iri(skosCloseMatch), right),
			pair{iri(pSignal), lit(string(dup.Signal))},
			pair{iri(pDetail), lit(dup.Detail)})
	}

	for i, g := range f.Guesses {
		pairs := []pair{
			{iri(rdfType), iri(clGuess)},
			{iri(pField), lit(g.Field)},
			{iri(pChosenAs), lit(g.ChosenAs)},
		}
		if g.Reason != "" {
			pairs = append(pairs, pair{iri(pReason), lit(g.Reason)})
		}
		// The alternatives are several triples under one predicate, because
		// they are a set of things that were not chosen and nothing depends on
		// their order. Contrast attrPairs, where a JSON array's order is part
		// of the value.
		for _, alt := range g.Alternatives {
			pairs = append(pairs, pair{iri(pAlternative), lit(alt)})
		}
		pairs = append(pairs, provPairs(g.Provenance)...)
		d.preds(iri(l.recordIRI(l.opts.RunID, "guess", i)), pairs...)
	}

	for i, u := range f.Unread {
		d.preds(iri(l.recordIRI(l.opts.RunID, "unread", i)),
			pair{iri(rdfType), iri(clUnread)},
			pair{iri(pSource), lit(u.Source)},
			pair{iri(pLocator), lit(u.Locator)},
			pair{iri(pReason), lit(u.Reason)})
	}

	if err := l.post(ctx, graph, &d); err != nil {
		return err
	}
	rep.Violations += len(f.Violations)
	rep.Duplicates += len(f.Duplicates)
	rep.Guesses += len(f.Guesses)
	rep.Unread += len(f.Unread)
	return nil
}

// writeSupersessions files what a result says is over, and changes nothing
// about the record it names.
//
// A triple store is the store that could act — DELETE WHERE over the retired
// subject is a single statement, and it would run in milliseconds. That is
// exactly why it does not: §4 means alchemy holds no graph, and a producer able
// to delete another producer's fact by naming it would be an unreviewed writer
// with write access to a customer's store.
//
// The sequence continues across batches, from what the report already counted,
// so two batches do not write two records under one IRI — which in RDF would
// merge them into one subject carrying both retirements' predicates.
func (l *Loader) writeSupersessions(ctx context.Context, graph string, batch []alchemy.Supersession, rep *Report) error {
	var d doc
	for i, s := range batch {
		pairs := []pair{
			{iri(rdfType), iri(clSupersession)},
			// A literal, because the record being retired is usually not in
			// this result at all — it is in the store from a run that finished
			// last month, and minting an IRI for it here would assert that this
			// load contains it.
			{iri(pRetires), lit(s.Retires)},
		}
		if t, ok := l.refTerm(s.By); ok {
			pairs = append(pairs, pair{iri(pReplacedBy), t})
		}
		if s.Reason != "" {
			pairs = append(pairs, pair{iri(pReason), lit(s.Reason)})
		}
		pairs = append(pairs, provPairs(s.Provenance)...)
		d.preds(iri(l.recordIRI(l.opts.RunID, "supersession", rep.Supersessions+i)), pairs...)
	}
	if err := l.post(ctx, graph, &d); err != nil {
		return err
	}
	rep.Supersessions += len(batch)
	return nil
}

// writeRuleSets writes the standing policy a record can name.
//
// It is a count rather than a silence in the report because every record can
// carry a name into one, and a load that wrote none while its records named
// some is a graph whose provenance points nowhere.
func (l *Loader) writeRuleSets(ctx context.Context, graph string, sets []alchemy.RuleSet, rep *Report) error {
	var d doc
	for _, rs := range sets {
		self := iri(l.loadIRI(l.opts.RunID) + "/ruleset/" + escapeSegment(rs.Name))
		pairs := []pair{
			{iri(rdfType), iri(clRuleSet)},
			{iri(pName), lit(rs.Name)},
		}
		for i, r := range rs.Rules {
			rule := iri(l.loadIRI(l.opts.RunID) + "/ruleset/" + escapeSegment(rs.Name) + "/rule/" + escapeSegment(fmt.Sprint(i)))
			pairs = append(pairs, pair{iri(pRule), rule})
			rp := []pair{
				{iri(rdfType), iri(clRule)},
				{iri(pName), lit(r.Name)},
			}
			if r.Told != "" {
				rp = append(rp, pair{iri(pTold), lit(r.Told)})
			}
			d.preds(rule, rp...)
		}
		// Named by the rule set rather than numbered, because
		// alchemy.Provenance.RuleSet points at it by name: an IRI derived from
		// the position in the slice would move if the sets were reordered, and
		// every record's pointer would then name a different policy.
		d.preds(self, pairs...)
	}
	if err := l.post(ctx, graph, &d); err != nil {
		return err
	}
	rep.RuleSets += len(sets)
	return nil
}

// writeModelCalls records what the job spent.
//
// It is deliberately not part of sink.Digest — two runs that bought the same
// answer from a cache spent differently (§8.2) — so this is written and never
// compared. It is here because §7.2's rule is that "a failed job that reports
// no calls makes an expensive retry look free", and a buyer holding the graph
// is the one deciding whether to re-run.
func (l *Loader) writeModelCalls(ctx context.Context, graph string, calls []alchemy.ModelCall) error {
	var d doc
	for i, c := range calls {
		pairs := []pair{
			{iri(rdfType), iri(clModelCall)},
			{iri(pModel), lit(c.Model)},
			{iri(pStage), lit(c.Stage)},
			{iri(pCalls), intLit(c.Calls)},
		}
		if c.Tokens != 0 {
			pairs = append(pairs, pair{iri(pTokens), intLit(c.Tokens)})
		}
		d.preds(iri(l.recordIRI(l.opts.RunID, "modelcall", i)), pairs...)
	}
	return l.post(ctx, graph, &d)
}

// refTerm renders an alchemy.Ref as the thing it names, where this load holds
// it.
//
// An entity becomes its IRI. A relation becomes the QUOTED TRIPLE — which is
// the second place RDF-star earns its keep in this connector: the term a
// violation points at is exactly the term that violation's edge carries its
// provenance on, so "what is wrong with this claim" and "where did this claim
// come from" are two predicates of one subject rather than a join between two
// descriptions of one edge.
//
// A Ref this load cannot resolve reports false and the caller writes nothing,
// rather than minting an IRI for a record that is not here. The finding's
// Subject field still says in words what it was about, which is what a person
// reads.
func (l *Loader) refTerm(r alchemy.Ref) (term, bool) {
	switch r.Kind {
	case alchemy.RefEntity:
		if r.ID == "" {
			return term{}, false
		}
		return iri(l.entityIRI(l.opts.RunID, r.ID)), true
	case alchemy.RefRelation:
		if r.From == "" || r.To == "" || r.Type == "" {
			return term{}, false
		}
		return quoted(
			iri(l.entityIRI(l.opts.RunID, r.From)),
			iri(l.relIRI(r.Type)),
			iri(l.entityIRI(l.opts.RunID, r.To)),
		), true
	}
	return term{}, false
}
