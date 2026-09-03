package dgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// This connector as a sink.Sink.
//
// The load marker is what makes the three states of a name distinguishable, and
// they have to be: a name that is free, a name holding THIS graph, and a name
// holding a DIFFERENT one are three different answers and only the middle one
// is a no-op. The marker carries the digest, so the question is a lookup rather
// than a comparison of the graph.
//
// It also carries `complete`. A load that died halfway has its nodes and no
// completion, which is one query to find and one re-Load to fix — and it is
// what recall's reads check before answering, because a partial graph reported
// as a whole one is the confident wrong answer this design is arranged against.
var _ sink.Sink = (*Loader)(nil)

// Begin opens a load.
func (l *Loader) Begin(ctx context.Context, id sink.Ident) (sink.Tx, error) {
	if id.Load == "" {
		return nil, ErrNoRunID
	}
	// The Loader a caller holds is not the one that does the work: sink.Load
	// may open several loads through one Sink, and a Loader carrying a RunID
	// from a previous one would write this graph under that name.
	own := *l
	own.opts.RunID = id.Load

	marker, err := own.readMarker(ctx, id.Load)
	if err != nil {
		return nil, err
	}
	tx := &loadTx{l: &own, rep: Report{Load: id.Load, Digest: id.Digest}}
	switch {
	case marker == nil:
		// A free name.
	case marker.Digest == id.Digest && marker.Complete:
		// This graph, finished. Nothing to do, and saying so is what makes a
		// retried nightly import cost nothing.
		tx.converged = true
		return tx, nil
	case marker.Digest == id.Digest:
		// This graph, half written. Every write below is an upsert keyed on an
		// xid derived from the load, so re-running converges on what is there
		// — which is what makes a crashed load finishable by running it again.
	case id.Replace:
		if err := own.dropLoad(ctx, id.Load); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: load %q holds a graph with digest %s, this result is %s; "+
			"use a new load name, or Replace to overwrite it",
			sink.ErrExists, id.Load, short(marker.Digest), short(id.Digest))
	}
	if err := own.claim(ctx, id.Load, id.Digest); err != nil {
		return nil, err
	}
	return tx, nil
}

func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// marker is the run node read back.
type marker struct {
	Digest   string `json:"digest"`
	Complete bool   `json:"complete"`
}

// readMarker returns the load's marker, or nil when the name is free.
func (l *Loader) readMarker(ctx context.Context, load string) (*marker, error) {
	q := "{ q(func: eq(" + l.pred(keyXID) + ", " + literal(runXID(load)) + ")) {\n" +
		"  digest: " + l.pred(keyDigest) + "\n" +
		"  complete: " + l.pred(keyComplete) + "\n} }\n"
	var out struct {
		Q []marker `json:"q"`
	}
	if err := l.queryInto(ctx, q, &out); err != nil {
		return nil, fmt.Errorf("dgraph: reading the marker of load %q: %w", load, err)
	}
	if len(out.Q) == 0 {
		return nil, nil
	}
	return &out.Q[0], nil
}

// claim writes the marker that says this load is in progress under this digest.
func (l *Loader) claim(ctx context.Context, load, digest string) error {
	xid := runXID(load)
	var b strings.Builder
	b.WriteString(nquad("uid(v)", l.pred(keyXID), literal(xid)))
	b.WriteString(nquad("uid(v)", l.pred(keyRun), literal(load)))
	b.WriteString(nquad("uid(v)", l.pred(keyKind), literal(kindRun)))
	b.WriteString(nquad("uid(v)", l.pred(keyDigest), literal(digest)))
	b.WriteString(nquad("uid(v)", l.pred(keyComplete), boolLit(false)))
	return l.mutate(ctx, l.upsert(xid, sortedQuads(b.String())))
}

// dropLoad removes every node of one load.
//
// `delete { uid(v) * * . }` rather than a per-predicate delete: the second
// would need this connector to know every predicate any version of it ever
// wrote, and a load written by an older build would leave whatever that build
// knew about behind — a graph that is neither the old one nor the new one.
//
// The edges go with the nodes. Dgraph deletes an edge when either endpoint is
// deleted, which is what makes this one mutation instead of two passes.
func (l *Loader) dropLoad(ctx context.Context, load string) error {
	body := "upsert {\n query { v as var(func: eq(" + l.pred(keyRun) + ", " + literal(load) + ")) }\n" +
		" mutation { delete {\n  uid(v) * * .\n } }\n}\n"
	if err := l.mutate(ctx, body); err != nil {
		return fmt.Errorf("dgraph: dropping load %q: %w", load, err)
	}
	return nil
}

// loadTx is one load in progress.
type loadTx struct {
	l         *Loader
	rep       Report
	converged bool
}

func (t *loadTx) Converged() bool { return t.converged }

func (t *loadTx) Entities(ctx context.Context, batch []alchemy.Entity) error {
	if t.converged || len(batch) == 0 {
		return nil
	}
	t.rep.Batches++
	return t.l.writeEntities(ctx, batch, &t.rep)
}

func (t *loadTx) Relations(ctx context.Context, batch []alchemy.Relation) error {
	if t.converged || len(batch) == 0 {
		return nil
	}
	t.rep.Batches++
	return t.l.writeRelations(ctx, batch, &t.rep)
}

func (t *loadTx) Chunks(ctx context.Context, batch []sink.Chunk) error {
	if t.converged || len(batch) == 0 {
		return nil
	}
	t.rep.Batches++
	return t.l.writeChunks(ctx, batch, &t.rep)
}

func (t *loadTx) Findings(ctx context.Context, f sink.Findings) error {
	if t.converged {
		return nil
	}
	t.rep.Batches++
	return t.l.writeFindings(ctx, f, &t.rep)
}

func (t *loadTx) Supersessions(ctx context.Context, ss []alchemy.Supersession) error {
	if t.converged || len(ss) == 0 {
		return nil
	}
	t.rep.Batches++
	return t.l.writeSupersessions(ctx, ss, &t.rep)
}

// Commit marks the load complete and files the numbers §5 obliges it to carry.
//
// The completion is a separate write from the claim, and the gap between them
// is the whole point: a load that died in the middle has a marker with
// complete=false, which is how recall tells a half-written graph from a whole
// one and refuses to answer from it.
func (t *loadTx) Commit(ctx context.Context, s sink.Summary) (sink.Report, error) {
	rep := t.report()
	if t.converged {
		rep.Converged = true
		return rep, nil
	}
	counts, err := json.Marshal(s.Counts)
	if err != nil {
		return rep, fmt.Errorf("dgraph: rendering counts: %w", err)
	}
	xid := runXID(t.l.opts.RunID)
	var b strings.Builder
	b.WriteString(nquad("uid(v)", t.l.pred(keyXID), literal(xid)))
	b.WriteString(nquad("uid(v)", t.l.pred(keyComplete), boolLit(true)))
	b.WriteString(nquad("uid(v)", t.l.pred(keyAttrs), literal(string(counts))))
	if t.l.opts.Ontology != nil {
		b.WriteString(nquad("uid(v)", t.l.pred(keyOntology), literal(t.l.opts.Ontology.ID)))
	}
	t.rep.Batches++
	if err := t.l.mutate(ctx, t.l.upsert(xid, sortedQuads(b.String()))); err != nil {
		return rep, fmt.Errorf("dgraph: completing load %q: %w", t.l.opts.RunID, err)
	}
	return t.report(), nil
}

// Abort ends a load without finishing it, and deliberately removes nothing.
//
// What was written stays, with complete=false beside it. §8.3's takeover needs
// to find a half-written load saying it is half-written; a store that cleaned
// up after itself would leave the next node with no evidence that anything had
// happened.
func (t *loadTx) Abort(ctx context.Context) error { return nil }

func (t *loadTx) report() sink.Report {
	r := sink.Report{
		Load: t.rep.Load, Digest: t.rep.Digest,
		Entities: t.rep.Entities, Relations: t.rep.Relations, Chunks: t.rep.Chunks,
		Violations: t.rep.Violations, Duplicates: t.rep.Duplicates,
		Guesses: t.rep.Guesses, Unread: t.rep.Unread,
		Supersessions: t.rep.Supersessions, Batches: t.rep.Batches,
	}
	if t.rep.SkippedVectors > 0 {
		r.Lost = append(r.Lost, sink.Loss{
			What: "vectors", Count: t.rep.SkippedVectors,
			Why: "Dgraph holds no vector index this connector can search, so an embedding " +
				"stored here would answer no question; the chunk text is kept and the citation resolves",
		})
	}
	if t.rep.MergedRelations > 0 {
		r.Lost = append(r.Lost, sink.Loss{
			What: "relations", Count: t.rep.MergedRelations,
			Why: "a Dgraph edge carries one set of facets, so two records asserting the same " +
				"(from, type, to) cannot both keep their provenance; the first is kept",
		})
	}
	return r
}
