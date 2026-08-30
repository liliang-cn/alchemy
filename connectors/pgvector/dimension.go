package pgvector

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// DimensionError is the refusal that keeps a vector store honest.
//
// The dimension is a job-time fact — alchemy.Vector.Values is whatever the
// caller's embedding model returned — and `vector(n)` needs n at DDL time. The
// two ways to make that disappear are both failures that look like successes:
// truncating to the bound width answers queries with vectors that no longer
// mean what the model said, and creating a second table answers them with half
// the corpus and no error. So a mismatch is an error with both numbers and the
// model's name in it, and the load writes nothing.
type DimensionError struct {
	// Bound is what the schema's embedding column is declared as, 0 when the
	// schema has not been bound yet.
	Bound int
	// Found is what the result carries.
	Found int
	// Model is the embedding model that produced Found, so that a buyer with
	// two pipelines can tell which one to point somewhere else.
	Model string
	// Where says which part of the result disagreed, for the case where a
	// single result is not internally consistent.
	Where string
}

func (e *DimensionError) Error() string {
	if e.Bound == 0 {
		return fmt.Sprintf("pgvector: %s: this result does not have one dimension, so there is none to bind: %d and %d",
			e.Where, e.Found, e.Bound)
	}
	if e.Where != "" {
		return fmt.Sprintf("pgvector: %s: vectors of %d and %d dimensions in one result (model %q); "+
			"a result whose vectors disagree cannot be stored under either",
			e.Where, e.Bound, e.Found, e.Model)
	}
	return fmt.Sprintf("pgvector: this schema stores vector(%d) and the result carries vector(%d) from model %q; "+
		"nothing was written. Load it into a schema of its own, or re-embed the corpus with one model — "+
		"truncating would answer queries with vectors that mean something else",
		e.Bound, e.Found, e.Model)
}

// BoundDimension reports the width the embedding column is declared at, or 0
// if the schema has not been bound yet.
//
// It reads the catalog rather than a row the connector wrote, because the
// column's own declaration is the only copy that cannot drift from the data in
// it. A remembered number and a real column that disagree is a store that
// reports a dimension it does not have.
func (l *Loader) BoundDimension(ctx context.Context) int {
	n, err := l.boundDimension(ctx)
	if err != nil {
		return 0
	}
	return n
}

func (l *Loader) boundDimension(ctx context.Context) (int, error) {
	return boundIn(ctx, l.pool, l.schema)
}

// bindDimension makes the schema's embedding column exist at width n, or
// refuses because it already exists at some other width.
//
// It runs under the same advisory lock as Migrate, so two processes loading
// the first two results of a corpus at once produce one column rather than one
// column and one error — and the loser of that race sees its dimension already
// bound, which is the same check it would have made anyway.
func (l *Loader) bindDimension(ctx context.Context, n int, model string) error {
	if n <= 0 {
		return nil
	}
	return l.withDDLLock(ctx, func(tx pgx.Tx) error {
		mod, err := boundIn(ctx, tx, l.schema)
		if err != nil {
			return err
		}
		if mod == n {
			return nil
		}
		if mod != 0 {
			return &DimensionError{Bound: mod, Found: n, Model: model}
		}
		// Adding a nullable column with no default is a catalog-only change,
		// so a corpus that has been loading for an hour is not rewritten by
		// the first result that brings vectors.
		add := fmt.Sprintf("ALTER TABLE %s.chunks ADD COLUMN embedding vector(%d)", l.schema, n)
		if _, err := tx.Exec(ctx, add); err != nil {
			return fmt.Errorf("pgvector: %s: %w", add, err)
		}
		// The view was created without the column and would keep hiding it.
		if _, err := tx.Exec(ctx, chunkViewSQL(l.schema, true)); err != nil {
			return fmt.Errorf("pgvector: recreating loaded_chunks: %w", err)
		}
		return nil
	})
}

// dimensionOf is the check a result has to pass before any of it is written:
// one dimension, no empty vectors, and every vector naming a chunk that exists.
//
// All three are things alchemy.Result does not itself promise. Vector.Values is
// a slice per vector with nothing tying two of them together, and Vector.Chunk
// is an index into a slice the type does not require to be present — so the
// connector computes the result's dimension rather than reading it, and says so
// when it cannot.
func dimensionOf(res alchemy.Result) (int, string, error) {
	chunks := make(map[int]bool, len(res.Chunks))
	for _, c := range res.Chunks {
		chunks[c.Index] = true
	}
	dim, model := 0, ""
	for _, v := range res.Vectors {
		if len(v.Values) == 0 {
			return 0, "", fmt.Errorf("pgvector: the vector for chunk %d is empty; "+
				"an embedding nobody can search is not one worth storing", v.Chunk)
		}
		if !chunks[v.Chunk] {
			return 0, "", fmt.Errorf("pgvector: a vector names chunk %d and the result has no such chunk; "+
				"storing it would leave an embedding with no text behind it", v.Chunk)
		}
		if dim == 0 {
			dim, model = len(v.Values), v.Model
			continue
		}
		if len(v.Values) != dim {
			return 0, "", &DimensionError{
				Bound: dim, Found: len(v.Values), Model: v.Model,
				Where: fmt.Sprintf("chunk %d", v.Chunk),
			}
		}
	}
	return dim, model, nil
}
