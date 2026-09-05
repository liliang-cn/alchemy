package cortexdb

import (
	"context"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Loader is a sink.Sink. §4.1 ended §4's deferral once four consumers existed,
// and this is the half of this connector that turned out to be one thing
// written four times: an identity, an envelope, a report, and the refusals in
// front of them.
//
// What stayed is what CortexDB's own model decides. Its edge identity is
// (from, to, type, document) — not the interface's and not this package's, but
// the store's, taken from graphrag_tool_ingest — and grouping before the write
// is what stops its merge from overwriting a provenance. So are the reserved
// property names, the document-per-source shape, the strict-ontology refusal
// and the re-key guard.
var _ sink.Sink = (*Loader)(nil)

// Begin opens a run: it refuses a store this load cannot honestly land in, and
// writes the marker that makes a half-written run detectable.
func (l *Loader) Begin(ctx context.Context, id sink.Ident) (sink.Tx, error) {
	l.opts.RunID = id.Load
	rep := l.report
	if rep == nil {
		// A caller driving this store through sink.Load directly rather than
		// through Load. Nothing reads the numbers, so they go somewhere.
		rep = &Report{Run: id.Load}
	}
	t := &tx{l: l, p: l.plan, rep: rep, written: map[int]bool{}}
	if t.p == nil {
		// A caller driving this store through sink.Load directly: the plan is
		// built from the stream, a batch at a time.
		t.p = newPlan(l.opts, id.Digest)
	} else {
		// Load already built one, checking the whole result so that its typed
		// refusals arrive before the marker does. Reusing it rather than
		// building a second is the difference between holding this graph once
		// and holding it twice, which on §8's import is the difference the
		// envelope exists to make.
		t.filed = true
	}

	if err := l.checkStore(ctx); err != nil {
		return nil, err
	}
	done, err := l.claimRun(ctx, id.Digest, id.Replace, rep)
	if err != nil {
		return nil, err
	}
	t.converged = done
	return t, nil
}

// tx is one run in progress, and it is the connector where the streaming
// envelope buys the least — which is worth saying plainly rather than hiding
// in the shape.
//
// Two of CortexDB's own rules force it. Its edge identity is (from, to, type,
// document), so two records that are one edge can be pages apart and the store
// has to see both before it writes either; and an entity carries the ids of the
// chunks it was extracted from, but only for chunks that were actually written
// — a chunk with no embedding is not written here, and a chunk id that resolved
// to nothing would make FactProvenance report a hole. sink.Tx sends entities
// before relations before chunks, which is what the other three stores need, so
// this one cannot write a node until every chunk has arrived.
//
// So the records buffer and the text and the embeddings do not. That is a real
// reduction — on a corpus, the chunk text and the vectors are almost all of the
// bytes — and it is not the property the envelope advertises. It is reported as
// a finding rather than smoothed over: a store whose identity model needs the
// whole edge set is a store that cannot stream edges, and no interface can
// change that.
type tx struct {
	l *Loader
	p *plan

	converged bool
	written   map[int]bool
	// wroteChunks is how far into plan.chunks the store has been written, so a
	// batch writes only what arrived in it.
	wroteChunks int
	// filed says the plan already holds every record, because Load built it.
	// The batches still arrive — the envelope does not know the difference —
	// and filing them again would double the graph.
	filed bool
	rep   *Report

	findings sink.Findings
	// retired is what this result says is over. It accumulates like the
	// findings do and for the same reason -- the completion document is
	// written once, at the end -- and it is a separate field rather than one
	// more key in the findings blob because it is not a finding.
	retired []alchemy.Supersession
}

// Converged is true only for a run that is already there and already finished.
// An unfinished one with this digest is the crashed load Incomplete() reports,
// and finishing it means writing again.
func (t *tx) Converged() bool { return t.converged }

func (t *tx) Entities(_ context.Context, batch []alchemy.Entity) error {
	if t.filed {
		return nil
	}
	return t.p.addEntities(batch)
}

func (t *tx) Relations(_ context.Context, batch []alchemy.Relation) error {
	if t.filed {
		return nil
	}
	return t.p.addRelations(batch)
}

// Chunks is the one kind that reaches the store as it streams: a chunk is an
// embedding row and a document, and neither needs anything that has not
// arrived. The documents are created per batch and the creation is idempotent,
// because a source can appear in a chunk this batch and an entity two batches
// ago.
func (t *tx) Chunks(ctx context.Context, batch []sink.Chunk) error {
	if !t.filed {
		if err := t.p.addChunks(batch); err != nil {
			return err
		}
	}
	if err := t.l.writeDocuments(ctx, t.p, t.rep); err != nil {
		return err
	}
	written, err := t.l.writeChunks(ctx, t.p, t.wroteChunks, t.rep)
	t.wroteChunks = len(t.p.chunks)
	for i := range written {
		t.written[i] = true
	}
	return err
}

// Findings is where the knowledge contract's one reachable refusal arrives.
//
// The envelope sends them "after the records they are about, so a store that
// links a violation to its subject finds the subject already there" — and this
// store writes its graph at Commit, which is after that again. So a violation
// naming a record in fields (Violation.About) is in hand before the node it
// grades is written, and `_grade=refused` with the ontology's own reason beside
// it is a fact this connector holds rather than one it would have to infer. See
// contract.go for the three rows of the spec's table that are not.
func (t *tx) Findings(_ context.Context, f sink.Findings) error {
	t.findings = f
	t.p.refuse(f.Violations)
	return nil
}

// Supersessions files what the result says is over. It rides on the run's
// completion document beside the findings and not among them, and nothing acts
// on it: no node is deleted, no edge is detached, and the record named in
// Retires is exactly as the run that wrote it left it.
//
// A document rather than a graph node, for the reason the run marker is one: a
// CortexDB graph node needs a vector, and the only vector this connector could
// put on a retirement is one it made up. Fabricating an embedding to hold
// bookkeeping is what the rest of this package refuses to do even for real
// text.
func (t *tx) Supersessions(_ context.Context, batch []alchemy.Supersession) error {
	t.retired = append(t.retired, batch...)
	return nil
}

// Commit is where this store's graph is actually written, for the reason the
// type comment gives: the nodes need the chunk ids and the edges need every
// member of their group, and neither is knowable until the stream has ended.
func (t *tx) Commit(ctx context.Context, s sink.Summary) (sink.Report, error) {
	if t.converged {
		return sink.Report{Load: t.l.opts.RunID, Digest: t.p.digest, Batches: t.rep.Batches}, nil
	}
	// The check that cannot be made per batch: two records CortexDB calls one
	// edge, carrying two different producer keys. Load has already made it on
	// the plan it built; a streaming caller reaches it here, which is the last
	// moment it can be made at all.
	if !t.filed {
		if err := t.p.checkParallelEdges(); err != nil {
			return sink.Report{}, err
		}
	}
	t.rep.SkippedRelations, t.rep.FusedRelations = t.p.skipped, t.p.fused
	if err := t.l.writeDocuments(ctx, t.p, t.rep); err != nil {
		return sink.Report{}, err
	}
	if err := t.l.writeEntities(ctx, t.p, t.written, t.rep); err != nil {
		return sink.Report{}, err
	}
	if err := t.l.writeRelations(ctx, t.p, t.written, t.rep); err != nil {
		return sink.Report{}, err
	}
	t.rep.Supersessions = len(t.retired)
	if err := t.l.completeRun(ctx, t.p.digest, s, t.findings, t.retired, t.rep); err != nil {
		return sink.Report{}, err
	}
	return sink.Report{
		Load: t.l.opts.RunID, Digest: t.p.digest, Batches: t.rep.Batches, Lost: t.lost(),
	}, nil
}

// Abort leaves the marker with no completion beside it, which is exactly what
// Incomplete() reports and a re-Load finishes. Nothing is removed: a run that
// died is what §8.3's takeover has to find.
func (t *tx) Abort(context.Context) error { return nil }

// lost is what this store could not keep.
//
// §4.1 puts Lost above the line although only the vector store needed it, and
// this is a third store with an answer. CortexDB holds a chunk's text in a
// vector row, so a chunk with no embedding has nowhere to go — and inventing an
// embedding to carry the text is the recomputation this connector refuses.
func (t *tx) lost() []sink.Loss {
	if t.rep.ChunksWithoutVectors == 0 {
		return nil
	}
	return []sink.Loss{{
		What: "chunks", Count: t.rep.ChunksWithoutVectors,
		Why: "CortexDB stores a chunk's text in a vector row, so a chunk alchemy did not embed has nowhere " +
			"to go; its text is not in the store and a citation naming it will not resolve — embed the result " +
			"before loading it if the text is what the corpus is for",
	}}
}
