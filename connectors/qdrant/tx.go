package qdrant

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
// What stayed is what only this store can answer. A point ID must be a UUID or
// an unsigned integer, so every record here is addressed by a derived value
// (see pointID) — that is the store's idempotency mechanism, and it has nothing
// in common with a MERGE on a key or an ON CONFLICT DO UPDATE. So are the
// nested payload, the collection's fixed width, the payload indexes and the
// filter surface.
var _ sink.Sink = (*Loader)(nil)

// Begin opens a load: it creates the collection at this result's width, decides
// what an existing load of this name means, and writes the marker that makes a
// half-written load detectable.
//
// The collection is created here because there is no later moment — Qdrant
// fixes the width at creation and has no ALTER — and the marker is written
// before the first data point, so there is no window in which points are
// present and nothing knows they are partial.
func (l *Loader) Begin(ctx context.Context, id sink.Ident) (sink.Tx, error) {
	dim := id.Vectors.Dimension
	t := &tx{l: l, id: id.Load, digest: id.Digest, dim: dim, ends: newEndpoints()}

	if err := l.ensure(ctx, dim, id.Vectors.Model); err != nil {
		return nil, err
	}
	prior, err := l.marker(ctx, id.Load)
	if err != nil {
		return nil, err
	}
	if !id.Replace {
		if prior != nil && prior.Fingerprint == id.Digest && prior.Complete {
			t.converged = true
			return t, nil
		}
		if prior != nil {
			return nil, &ConflictingLoadError{ID: id.Load, Have: prior.Fingerprint, Want: id.Digest, Complete: prior.Complete}
		}
		// A different name over the same graph is still the same graph. Doing
		// it again under a second name would double every answer for a corpus
		// nobody changed, which is the one thing idempotency has to prevent.
		if name, ok, err := l.completeFingerprint(ctx, id.Digest); err != nil {
			return nil, err
		} else if ok {
			t.id, t.converged = name, true
			return t, nil
		}
	} else if prior != nil {
		if err := l.deleteLoad(ctx, id.Load); err != nil {
			return nil, err
		}
	}
	if err := l.claim(ctx, t.id, id.Digest, dim); err != nil {
		return nil, err
	}
	return t, nil
}

// tx is one load in progress.
//
// ends is the price this store pays for having no joins, and it is the one
// thing the migration to a streaming envelope could not lift: a relation point
// carries its endpoints' names and a chunk point carries the ids of the
// entities extracted from it, because a reader here cannot follow an id, and
// both require having seen the entities. sink.Tx guarantees entities arrive
// first, so the index is built as they stream — names and ids, not the graph,
// and nothing about the chunks, the vectors or the text is held.
type tx struct {
	l      *Loader
	id     string
	digest string
	dim    int

	converged bool
	ends      *endpoints

	relations int
	// supersessions is the count so far, which is the position the next batch
	// starts at: a load is a stream, so a claim's place in it is counted across
	// batches rather than read off a slice index.
	supersessions int
	// rep is this store's own tally. The driver overwrites the record counts
	// in the Report that Commit returns — it is what handed them over — but
	// the numbers are still needed here: the marker records how many points
	// landed, and what this store could not keep depends on how many chunks
	// arrived without an embedding.
	rep sink.Report

	guesses []alchemy.Guess
	unread  []alchemy.Unread
}

func (t *tx) Converged() bool { return t.converged }

func (t *tx) Entities(ctx context.Context, batch []alchemy.Entity) error {
	t.ends.add(batch)
	return t.put(ctx, entityPoints(t.id, t.digest, batch), &t.rep.Entities, len(batch))
}

func (t *tx) Relations(ctx context.Context, batch []alchemy.Relation) error {
	b := relationPoints(t.id, t.digest, t.relations, batch, t.ends)
	t.relations += len(batch)
	return t.put(ctx, b, &t.rep.Relations, len(batch))
}

func (t *tx) Chunks(ctx context.Context, batch []sink.Chunk) error {
	for _, c := range batch {
		if c.Vector != nil {
			t.rep.Vectors++
		}
	}
	return t.put(ctx, chunkPoints(t.id, t.digest, batch, t.ends), &t.rep.Chunks, len(batch))
}

func (t *tx) Findings(ctx context.Context, f sink.Findings) error {
	if err := t.put(ctx, violationPoints(t.id, t.digest, f.Violations), &t.rep.Violations, len(f.Violations)); err != nil {
		return err
	}
	if err := t.put(ctx, duplicatePoints(t.id, t.digest, f.Duplicates), &t.rep.Duplicates, len(f.Duplicates)); err != nil {
		return err
	}
	// Guesses and unread pages are not points. They are read whole by a person
	// looking at one load rather than filtered on, so they ride on the marker
	// at Commit — see complete. Counted here so the report says they arrived.
	t.rep.Guesses += len(f.Guesses)
	t.rep.Unread += len(f.Unread)
	t.guesses, t.unread = f.Guesses, f.Unread
	return nil
}

// Supersessions writes what the result says is over, as points beside the
// graph, and applies none of it. See supersessionPoints.
func (t *tx) Supersessions(ctx context.Context, batch []alchemy.Supersession) error {
	b := supersessionPoints(t.id, t.digest, t.supersessions, batch)
	t.supersessions += len(batch)
	return t.put(ctx, b, &t.rep.Supersessions, len(batch))
}

// put writes one kind's points and folds the request count into the report.
func (t *tx) put(ctx context.Context, b batchOf, into *int, n int) error {
	if len(b.points) == 0 {
		return nil
	}
	points, reqs, err := t.l.upsert(ctx, b)
	t.rep.Batches += reqs
	if err != nil {
		return err
	}
	_ = points
	*into += n
	return nil
}

// Commit flips the marker, which is the only statement that makes a load
// visible. §5's numbers are written here rather than at Begin, because a marker
// advertising counts while its points were still arriving would be a store
// making claims it could not yet answer with.
func (t *tx) Commit(ctx context.Context, s sink.Summary) (sink.Report, error) {
	t.rep.Load, t.rep.Digest = t.id, t.digest
	t.rep.Lost = losses(lost(t.dim, t.rep.Chunks, t.rep.Vectors))
	if t.converged {
		return t.rep, nil
	}
	if err := t.l.complete(ctx, t, s); err != nil {
		return sink.Report{}, err
	}
	t.rep.Batches++
	return t.rep, nil
}

// Abort leaves what was written where it is.
//
// Nothing is undone, and that is this store's answer rather than the
// interface's: the marker still says complete=false, every read here resolves
// the set of complete loads before it filters, and Loads() shows an operator
// exactly which load stopped and how much of it arrived — which is more useful
// than a rollback that would itself be a bulk operation able to fail halfway.
// sink.Tx asks only that the load be observable as unfinished, which it is.
func (t *tx) Abort(context.Context) error { return nil }

// losses turns this store's sentences into the interface's shape.
//
// The count is deliberately left at zero for the two standing entries: "a
// vector store holds no traversal" is true of the whole load rather than of a
// number of records, and a zero that reads as "none" would be worse than no
// number. Where there is a real count — the chunks nobody embedded — it is in
// the sentence, which is where a person reading Loads() finds it.
func losses(why []string) []sink.Loss {
	out := make([]sink.Loss, 0, len(why))
	for _, w := range why {
		out = append(out, sink.Loss{What: "graph", Why: w})
	}
	return out
}
