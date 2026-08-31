package rdf

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Loader is a sink.Sink. §4.1 ended §4's deferral once four consumers existed;
// this is the fifth, written against the interface rather than against the JSON,
// which is what the interface was extracted for.
//
// What is this store's own — the part §4.1 leaves below the line — is the
// answer to "what is one edge's provenance attached to", which in RDF is a
// question with four candidate answers and one that works (see the package
// doc), and the consequence that falls out of it: a graph is a set, so two
// assertions of one edge are one triple.
var _ sink.Sink = (*Loader)(nil)

// Begin opens a load: it claims the named graph, writes the marker that makes a
// half-written load detectable, and declares the vocabulary if the caller
// supplied one.
func (l *Loader) Begin(ctx context.Context, id sink.Ident) (sink.Tx, error) {
	// The load name comes off the envelope, which is where §4.1 puts result
	// identity. Options.RunID is what a caller of this package's own Load set,
	// and either way this is the name the graph is called.
	l.opts.RunID = id.Load
	rep := l.report
	if rep == nil {
		// A caller driving this store through sink.Load directly rather than
		// through this package's Load. The numbers still have to go somewhere,
		// and they still have to reach sink.Report.Batches — an operator whose
		// load died halfway needs to know how many round trips it had made,
		// whichever entry point they used.
		rep = &Report{Load: id.Load}
		l.report = rep
	}
	graph := l.loadIRI(id.Load)
	rep.Graph = graph

	replay, done, err := l.claimLoad(ctx, id.Digest, id.Replace)
	if err != nil {
		return nil, err
	}
	rep.Replay = replay
	t := &tx{
		l: l, graph: graph, digest: id.Digest, rep: rep,
		ids: map[string]bool{}, edges: map[string]string{},
		// A replay of an unfinished load is not a convergence. Every write here
		// is an insert into a set, so re-running them is how a load that died
		// halfway is finished — the opposite of skipping them. Only a load that
		// is already complete has nothing to do.
		converged: replay && done,
	}
	if t.converged {
		return t, nil
	}
	if err := l.writeOntology(ctx, graph); err != nil {
		return nil, err
	}
	return t, nil
}

// tx is one load in progress.
type tx struct {
	l      *Loader
	graph  string
	digest string
	// ids is the entity index the dangling check needs, and it is the one thing
	// that could not be lifted above the line: an edge whose endpoint is not in
	// the result is ViolationDanglingRelation, which §7.3 keeps rather than
	// refuses, and writing it would mint an IRI for an entity that does not
	// exist — which in RDF is not an error at all, merely a subject nothing
	// else says anything about. sink.Tx guarantees entities arrive before the
	// relations that name them, so the set is built as they stream.
	ids map[string]bool
	// edges is what RDF costs and a property graph does not. See Relations.
	edges     map[string]string
	converged bool
	rep       *Report
}

func (t *tx) Converged() bool { return t.converged }

func (t *tx) Entities(ctx context.Context, batch []alchemy.Entity) error {
	for _, e := range batch {
		t.ids[e.ID] = true
	}
	if err := t.l.writeEntities(ctx, t.graph, batch); err != nil {
		return err
	}
	t.rep.Entities += len(batch)
	return nil
}

// Relations writes the edges, and is where this store's one real limitation is
// enforced rather than papered over.
//
// Two things are dropped here and both are reported.
//
// A dangling edge — one whose endpoint is not in the result — is
// ViolationDanglingRelation, which §7.3 puts on the "graph delivered" side of
// the line, so it is skipped rather than fatal, and never quietly.
//
// The second is RDF's own. A graph is a SET of triples: <a> <USES> <b> written
// twice is one triple. Where neo4j keeps two parallel edges with two
// provenances — that is what its relationKey buys, and its comment says why:
// "a merged edge can name only one producer" — this store cannot. Annotating
// the one quoted triple with both provenances would put two sources and two
// producers on it with nothing saying which belongs to which, and a walk would
// return their cross product as four claims the corpus never made. So the first
// assertion is kept, the second is not written, and it is named in
// Report.MergedRelations and counted in Report.Lost.
//
// That is the honest price of RDF-star over reification, and it is worth
// stating which half is lost: the edge survives, with one provenance. What is
// gone is the second piece of evidence for it — a reader can no longer see that
// two chunks said the same thing, or which of them a given model produced.
func (t *tx) Relations(ctx context.Context, batch []alchemy.Relation) error {
	keep := make([]alchemy.Relation, 0, len(batch))
	for _, r := range batch {
		from, to := t.ids[r.From], t.ids[r.To]
		if !from || !to {
			t.rep.SkippedRelations = append(t.rep.SkippedRelations,
				fmt.Sprintf("%s -[%s]-> %s (%s)", r.From, r.Type, r.To, missing(from, to)))
			continue
		}
		// The key is the triple, because the triple is what the store holds one
		// of. The value is the whole assertion, so an identical record — the
		// same result loaded twice within one stream — is recognised as a
		// repeat rather than reported as a loss.
		key := r.From + "\x00" + r.Type + "\x00" + r.To
		assertion := sink.Digest(alchemy.Result{Relations: []alchemy.Relation{r}})
		if prev, seen := t.edges[key]; seen {
			if prev != assertion {
				t.rep.MergedRelations = append(t.rep.MergedRelations,
					fmt.Sprintf("%s -[%s]-> %s (%s, %s)", r.From, r.Type, r.To,
						r.Provenance.Producer, marker(r.Provenance)))
			}
			continue
		}
		t.edges[key] = assertion
		keep = append(keep, r)
	}
	if err := t.l.writeRelations(ctx, t.graph, keep); err != nil {
		return err
	}
	t.rep.Relations += len(keep)
	return nil
}

// Chunks writes the text and drops the embedding; see writeChunks.
func (t *tx) Chunks(ctx context.Context, batch []sink.Chunk) error {
	// Counted before the skip, and counted here rather than from the result,
	// so the number is what reached this store rather than what a caller
	// happened to hold. It is the same number either way today; the difference
	// is that a caller driving the envelope directly — a paged reader that
	// never materialises an alchemy.Result — gets a truthful Lost as well.
	for _, c := range batch {
		if c.Vector != nil {
			t.rep.SkippedVectors++
		}
	}
	if t.l.opts.SkipChunks {
		return nil
	}
	if err := t.l.writeChunks(ctx, t.graph, batch); err != nil {
		return err
	}
	t.rep.Chunks += len(batch)
	return nil
}

func (t *tx) Findings(ctx context.Context, f sink.Findings) error {
	if t.l.opts.SkipFindings {
		return nil
	}
	return t.l.writeFindings(ctx, t.graph, f, t.rep)
}

// Supersessions files what the result says is over, without acting on it.
//
// A triple store is exactly the store that could act — DELETE WHERE over the
// retired subject is one statement — and that is the reason it must not. §4
// means alchemy holds no graph, and a producer able to delete another
// producer's fact by naming it would be an unreviewed writer with write access.
// What is owed is that the statement survives: a reader months later can see
// that somebody said the old answer was over, and name them.
func (t *tx) Supersessions(ctx context.Context, batch []alchemy.Supersession) error {
	return t.l.writeSupersessions(ctx, t.graph, batch, t.rep)
}

// Commit writes the policy and the model calls, then flips the marker, which is
// the only statement that makes a load readable.
func (t *tx) Commit(ctx context.Context, s sink.Summary) (sink.Report, error) {
	if err := t.l.writeRuleSets(ctx, t.graph, s.RuleSets, t.rep); err != nil {
		return sink.Report{}, err
	}
	if err := t.l.writeModelCalls(ctx, t.graph, s.ModelCalls); err != nil {
		return sink.Report{}, err
	}
	if err := t.l.completeLoad(ctx, t.digest, s.Counts); err != nil {
		return sink.Report{}, err
	}
	return sink.Report{
		Load: t.l.opts.RunID, Digest: t.digest, Batches: t.rep.Requests, Lost: t.lost(),
	}, nil
}

// Abort leaves the marker saying al:complete false.
//
// Nothing is removed, and that is this store's answer rather than the
// interface's: a load that died is exactly what §8.3's takeover has to find,
// every write here is an insert into a set, so the retry is the same command
// again. DROP GRAPH would be cheap here — it is one statement — and that is
// precisely why the temptation is worth naming: dropping it would destroy the
// evidence of how far the load got.
func (t *tx) Abort(context.Context) error { return nil }

// lost is what this store could not keep, in the envelope's shape.
func (t *tx) lost() []sink.Loss {
	var out []sink.Loss
	if t.rep.SkippedVectors > 0 {
		out = append(out, sink.Loss{
			What: "vectors", Count: t.rep.SkippedVectors,
			Why: "a triple store holds no embeddings: the chunks were written with their text, their byte " +
				"offsets and their provenance, and no similarity search will reach them — load the same " +
				"result into the store bought for vectors if that is what the corpus is for",
		})
	}
	if n := len(t.rep.SkippedRelations); n > 0 {
		out = append(out, sink.Loss{
			What: "relations", Count: n,
			Why: "these edges name an endpoint the result does not contain, so there was nothing to " +
				"attach them to; Report.SkippedRelations names each one",
		})
	}
	if n := len(t.rep.MergedRelations); n > 0 {
		out = append(out, sink.Loss{
			What: "relations", Count: n,
			Why: "an RDF graph is a set of triples, so a second record asserting an edge another record " +
				"already asserted is the same triple; the edge is in the store with the first record's " +
				"provenance and the second piece of evidence for it is not — Report.MergedRelations names each one",
		})
	}
	if t.l.opts.SkipChunks {
		out = append(out, sink.Loss{
			What: "chunks", Count: 0,
			Why: "Options.SkipChunks was set, so this load holds no text: every citation in it resolves " +
				"to nothing and no claim from it can be checked against its source",
		})
	}
	return out
}

func missing(from, to bool) string {
	switch {
	case !from && !to:
		return "neither endpoint is in this result"
	case !from:
		return "the subject is not in this result"
	default:
		return "the object is not in this result"
	}
}

// marker renders the citation a record carries, in recall.Mark's notation, so
// that a person reading a load report and a person reading a context pack are
// reading the same thing.
func marker(p alchemy.Provenance) string {
	if p.Source == "" {
		return "no citation"
	}
	if p.Chunk < 0 {
		return "[" + p.Source + "]"
	}
	return fmt.Sprintf("[%s#%d]", p.Source, p.Chunk)
}
