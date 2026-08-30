package pgvector

import (
	"context"
	"errors"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Loader is a sink.Sink. §4.1 ended §4's deferral once four consumers existed,
// and this is the half of this connector that turned out to be one thing
// written four times: an identity, an envelope, a report, and the refusals in
// front of them.
//
// What stayed here is what the four answered differently and were each right
// to: COPY as the write mechanism, a partial unique index as the idempotency
// mechanism, the late-bound `vector(n)` column, the HNSW/ivfflat policy, and the
// views a query reads. None of it is expressible in an interface that also has
// to describe a property graph.
var _ sink.Sink = (*Loader)(nil)

// Begin opens a load: it binds the vector width, decides what an existing load
// of this name means, and writes the row that makes a half-written load
// detectable.
//
// The order is the design, and it is the order this connector already had. The
// dimension is bound before the load row exists, so a refused width leaves no
// row behind; the row is written before the first record, so there is no window
// in which data is present and nothing knows it is partial.
func (l *Loader) Begin(ctx context.Context, id sink.Ident) (sink.Tx, error) {
	t := &tx{l: l, id: id.Load, digest: id.Digest, dim: id.Vectors.Dimension}

	if !id.Replace {
		// A finished load carrying this exact graph, under whatever name. The
		// caller is handed that name rather than the one it asked for: the
		// graph is the same graph, and inventing a second name for it would be
		// this store deciding that one import is two.
		if name, ok, err := l.completeFingerprint(ctx, id.Digest); err != nil {
			return nil, err
		} else if ok {
			t.id, t.converged = name, true
			return t, nil
		}
	}
	if err := l.bindDimension(ctx, id.Vectors.Dimension, id.Vectors.Model); err != nil {
		return nil, err
	}
	if err := l.claim(ctx, t.id, id.Digest, id.Vectors.Dimension, id.Replace); err != nil {
		// "That name is taken by this same graph, and it finished" is the
		// no-op a retry deserves; every other collision stays a refusal.
		if _, ok, e2 := l.alreadyComplete(ctx, t.id, id.Digest, err); e2 == nil && ok {
			t.converged = true
			return t, nil
		}
		return nil, err
	}
	return t, nil
}

// tx is one load in progress.
//
// It holds three counters and two slices and nothing else. The counters are
// what `seq` used to read off a slice index — the position of a record in the
// load, which is now counted across batches because the load is a stream. The
// slices are the guesses and the unread pages, which this store writes into the
// load row's summary blocks at completion rather than as rows of their own;
// they are small by construction (one per mapped column, one per unreadable
// page) and holding them is not holding the graph.
type tx struct {
	l      *Loader
	id     string
	digest string
	dim    int

	converged bool

	relations int
	rep       sink.Report

	guesses []alchemy.Guess
	unread  []alchemy.Unread
}

func (t *tx) Converged() bool { return t.converged }

func (t *tx) Entities(ctx context.Context, batch []alchemy.Entity) error {
	if err := t.l.writeEntityBatch(ctx, t.id, batch); err != nil {
		return err
	}
	t.rep.Entities += len(batch)
	t.rep.Batches++
	return nil
}

func (t *tx) Relations(ctx context.Context, batch []alchemy.Relation) error {
	if err := t.l.writeRelationBatch(ctx, t.id, t.relations, batch); err != nil {
		return err
	}
	t.relations += len(batch)
	t.rep.Relations += len(batch)
	t.rep.Batches++
	return nil
}

func (t *tx) Chunks(ctx context.Context, batch []sink.Chunk) error {
	if err := t.l.writeChunkBatch(ctx, t.id, t.dim, batch); err != nil {
		return err
	}
	t.rep.Chunks += len(batch)
	for _, c := range batch {
		if c.Vector != nil {
			t.rep.Vectors++
		}
	}
	t.rep.Batches++
	return nil
}

func (t *tx) Findings(ctx context.Context, f sink.Findings) error {
	if err := t.l.writeViolationBatch(ctx, t.id, f.Violations); err != nil {
		return err
	}
	if err := t.l.writeDuplicateBatch(ctx, t.id, f.Duplicates); err != nil {
		return err
	}
	t.guesses, t.unread = f.Guesses, f.Unread
	t.rep.Violations += len(f.Violations)
	t.rep.Duplicates += len(f.Duplicates)
	t.rep.Guesses += len(f.Guesses)
	t.rep.Unread += len(f.Unread)
	t.rep.Batches++
	return nil
}

// Commit is the last statement of a load and the only one that makes it
// visible. The summary blocks are written here rather than at Begin because a
// load row that carried §5's counts while its rows were still arriving would be
// a store advertising numbers it could not yet answer with.
func (t *tx) Commit(ctx context.Context, s sink.Summary) (sink.Report, error) {
	t.rep.Load, t.rep.Digest = t.id, t.digest
	if t.converged {
		return t.rep, nil
	}
	if err := t.l.complete(ctx, t.id, s, t.guesses, t.unread); err != nil {
		// A concurrent loader that committed the identical result first wins
		// the partial unique index. Its graph is this graph, so the right
		// answer is the one a sequential second load gets.
		if name, ok, e2 := t.l.completeFingerprint(ctx, t.digest); e2 == nil && ok && isUnique(err) {
			t.l.abandon(ctx, t.id)
			t.rep.Load, t.rep.Converged = name, true
			return t.rep, nil
		}
		return sink.Report{}, err
	}
	t.rep.Batches++
	return t.rep, nil
}

// Abort removes what this load wrote.
//
// Removing rather than leaving it is this store's own answer and not the
// interface's: every read here joins against the load row, so an abandoned
// load is already invisible, and what deleting buys is the disk back. A store
// whose partial writes are visible would have to leave the marker instead —
// which is why sink.Tx asks only that the load be observable as unfinished.
func (t *tx) Abort(ctx context.Context) error {
	if t.converged {
		return nil
	}
	t.l.abandon(ctx, t.id)
	return nil
}

// alreadyComplete turns "that name is taken by this same graph, and it
// finished" into the no-op a retry deserves.
func (l *Loader) alreadyComplete(ctx context.Context, id, digest string, cause error) (string, bool, error) {
	var ce *ConflictingLoadError
	if !errors.As(cause, &ce) || ce.Have != digest || ce.State != stateComplete {
		return "", false, cause
	}
	return id, true, nil
}
