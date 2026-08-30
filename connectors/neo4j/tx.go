package neo4j

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Loader is a sink.Sink. §4.1 ended §4's deferral once four consumers existed,
// and this is the half of this connector that turned out to be one thing
// written four times: an identity, an envelope, a report, and the refusals in
// front of them.
//
// What stayed is what only a property graph has an opinion about: MERGE on
// (run, id) as the idempotency mechanism, the label grouping Cypher forces
// because a label cannot be a parameter, quoteIdent as the one interpolation
// site, and the reserved prefix — which exists here because Neo4j properties
// are flat and does not exist in the vector stores because a nested payload
// makes the collision unreachable.
var _ sink.Sink = (*Loader)(nil)

// Begin opens a run: it creates the index the whole package reads through and
// writes the run node that makes a half-written load detectable.
//
// The run node is written incomplete and only later completed, so that the
// window in which the graph is partial is a window in which the graph says so.
func (l *Loader) Begin(ctx context.Context, id sink.Ident) (sink.Tx, error) {
	// The run name comes off the envelope. It is Options.RunID where a caller
	// set one and the job that produced the result where they did not (see
	// preflight), and either way the identity of a load is the envelope's
	// question (§4.1), answered here.
	l.opts.RunID = id.Load
	rep := l.report
	if rep == nil {
		// A caller driving this store through sink.Load directly rather than
		// through Load. Nothing reads the numbers, so they go somewhere.
		rep = &Report{Run: id.Load}
	}
	t := &tx{l: l, digest: id.Digest, ids: map[string]bool{}, rep: rep}

	if err := l.ensureIndex(ctx); err != nil {
		return nil, err
	}
	replay, done, err := l.claimRun(ctx, id.Digest, id.Replace)
	if err != nil {
		return nil, err
	}
	// A replay of an unfinished run is not a convergence. Every write here is a
	// MERGE keyed on identity, so re-running them is how a load that died
	// halfway is finished — which is the opposite of skipping them. Only a run
	// that is already complete has nothing to do.
	t.rep.Replay = replay
	t.converged = replay && done
	return t, nil
}

// tx is one run in progress.
//
// ids is the entity index the dangling check needs, and it is the one thing
// this migration could not lift above the line: an edge whose endpoint is not
// in the result is ViolationDanglingRelation, which §7.3 keeps rather than
// refuses, and writing it would put a MATCH in front of a node that does not
// exist. sink.Tx guarantees entities arrive before the relations that name
// them, so the set is built as they stream — ids, not the graph.
type tx struct {
	l         *Loader
	digest    string
	ids       map[string]bool
	converged bool
	// rep is Load's own Report, written through so that the numbers a caller
	// gets are what this store wrote rather than what the driver handed over.
	// With SkipChunks or SkipFindings those are deliberately different.
	rep *Report
}

// Converged is true only for a run that is already there and already finished.
//
// The distinction is this store's whole answer to idempotency and it is why the
// envelope asks rather than assumes. A complete run with this digest needs
// nothing rewritten. An *incomplete* one with this digest is the crashed load
// of §8.3, and the way to finish it is to run the same MERGEs again — so the
// writes must happen, and a store that reported convergence there would leave
// the half-written run half-written and call it a success.
func (t *tx) Converged() bool { return t.converged }

func (t *tx) Entities(ctx context.Context, batch []alchemy.Entity) error {
	for _, e := range batch {
		t.ids[e.ID] = true
	}
	return t.l.writeEntities(ctx, batch, t.rep)
}

func (t *tx) Relations(ctx context.Context, batch []alchemy.Relation) error {
	keep := make([]alchemy.Relation, 0, len(batch))
	for _, r := range batch {
		// A dangling relation is ViolationDanglingRelation, and §7.3 puts
		// violations on the "returned, graph delivered" side of the line: one
		// source said something the ontology does not allow, and the rest of
		// the graph is usable without it. So it is skipped rather than fatal —
		// and never quietly, because an edge that disappeared with no record of
		// its disappearance is the silent loss this design refuses.
		from, to := t.ids[r.From], t.ids[r.To]
		if !from || !to {
			t.rep.SkippedRelations = append(t.rep.SkippedRelations,
				fmt.Sprintf("%s -[%s]-> %s (%s)", r.From, r.Type, r.To, missing(from, to)))
			continue
		}
		keep = append(keep, r)
	}
	return t.l.writeRelations(ctx, keep, t.rep)
}

// Chunks writes the text and drops the embedding.
//
// This is the connector for the graph store; an embedding belongs in the store
// bought for embeddings. The number left behind is Report.SkippedVectors, which
// Load fills from the result — so it says how many the job produced rather than
// how many reached here, which is the number a buyer needs in order to go and
// load them somewhere else.
func (t *tx) Chunks(ctx context.Context, batch []sink.Chunk) error {
	return t.l.writeChunks(ctx, batch, t.rep)
}

func (t *tx) Findings(ctx context.Context, f sink.Findings) error {
	return t.l.writeFindings(ctx, f, t.rep)
}

// Supersessions files what the result says is over, without acting on it. See
// writeSupersessions for why a graph store that could act is exactly the one
// that must not.
func (t *tx) Supersessions(ctx context.Context, batch []alchemy.Supersession) error {
	return t.l.writeSupersessions(ctx, batch, t.rep)
}

// Commit writes the policy and flips the run marker, which is the only
// statement that makes a load complete.
func (t *tx) Commit(ctx context.Context, s sink.Summary) (sink.Report, error) {
	if err := t.l.writeRuleSets(ctx, s.RuleSets, t.rep); err != nil {
		return sink.Report{}, err
	}
	if err := t.l.completeRun(ctx, t.digest, s.Counts, t.rep); err != nil {
		return sink.Report{}, err
	}
	return sink.Report{
		Load: t.l.opts.RunID, Digest: t.digest, Batches: t.rep.Batches,
		Lost: t.lost(),
	}, nil
}

// Abort leaves the run node saying complete=false.
//
// Nothing is removed, and that is this store's answer rather than the
// interface's: a run that died is exactly what §8.3's takeover has to find, and
// every write here is a MERGE, so the retry is the same command again. A
// DETACH DELETE over a four-hundred-thousand-node run would itself be the bulk
// operation that can fail halfway.
func (t *tx) Abort(context.Context) error { return nil }

// lost is what this store could not keep, in the envelope's shape.
//
// §4.1 puts Lost above the line although only the vector store needed it, and
// this is the second store to have an answer — which is the argument for having
// put it there. A graph store holds no embeddings, and a buyer who loaded a
// corpus here and searched it by similarity would find nothing and have been
// told nothing.
func (t *tx) lost() []sink.Loss {
	var out []sink.Loss
	if t.rep.SkippedVectors > 0 {
		out = append(out, sink.Loss{
			What: "vectors", Count: t.rep.SkippedVectors,
			Why: "a graph store holds no embeddings: the chunks were written with their text and their " +
				"provenance, and no similarity search will reach them — load the same result into the store " +
				"bought for vectors if that is what the corpus is for",
		})
	}
	if n := len(t.rep.SkippedRelations); n > 0 {
		out = append(out, sink.Loss{
			What: "relations", Count: n,
			Why: "these edges name an endpoint the result does not contain, so there was no node to " +
				"attach them to; they are reported rather than written, and Report.SkippedRelations names each one",
		})
	}
	return out
}
