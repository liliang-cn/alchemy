package pgvector

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The dimension is a job-time fact and vector(n) needs n at DDL time. The
// answer is that the first result carrying vectors binds the column, because
// the alternative — asking a buyer to declare the dimension of an embedding
// model they have not run yet — is asking them to guess.
func TestFirstLoadBindsTheDimension(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()

	var typ string
	f.scalar(t, &typ, `SELECT count(*)::text FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'chunks' AND column_name = 'embedding'`, f.schema)
	if typ != "0" {
		t.Fatalf("before any load the embedding column exists (%s); an unbound schema should have no column to be wrong about", typ)
	}

	if _, err := l.Load(ctx, smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := l.BoundDimension(ctx); got != 8 {
		t.Fatalf("bound dimension = %d, want 8", got)
	}
	f.scalar(t, &typ, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		WHERE attrelid = ($1 || '.chunks')::regclass AND attname = 'embedding'`, f.schema)
	if typ != "vector(8)" {
		t.Errorf("column type = %q, want vector(8)", typ)
	}
}

// A second result with a different dimension is the case where silence is a
// failure that looks like a success. Truncating would answer queries with
// vectors that mean nothing; a second table would answer them with half the
// corpus and no error.
func TestSecondDimensionIsRefusedWithBothNumbers(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()

	if _, err := l.Load(ctx, smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("first load: %v", err)
	}
	other := smallResult(16)
	other.Entities[0].Name = "SuperAI 2" // a genuinely different result, not a replay
	_, err := l.Load(ctx, other, LoadOptions{})
	var de *DimensionError
	if !errors.As(err, &de) {
		t.Fatalf("load of a 16-dimensional result into an 8-dimensional schema: err = %v, want *DimensionError", err)
	}
	if de.Bound != 8 || de.Found != 16 {
		t.Errorf("DimensionError = %+v, want Bound 8 Found 16", de)
	}
	if de.Model != "embed-4" {
		t.Errorf("DimensionError.Model = %q; the message has to name the model, or the buyer cannot tell which run to fix", de.Model)
	}
	// The refusal has to be total. A refused load that left its load row
	// behind would be a schema that quietly accumulates rubble.
	if n := f.count(t, "loads"); n != 1 {
		t.Errorf("loads = %d, want 1: the refused load must leave nothing behind", n)
	}
	if n := f.count(t, "entities"); n != 2 {
		t.Errorf("entities = %d, want 2", n)
	}
}

// One result whose vectors disagree with each other is refused before the
// schema is touched at all, because there is no dimension to bind.
func TestMixedDimensionsWithinOneResultAreRefused(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Vectors[1].Values = unit(16, 1)
	_, err := l.Load(context.Background(), res, LoadOptions{})
	var de *DimensionError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DimensionError", err)
	}
	if n := f.count(t, "loads"); n != 0 {
		t.Errorf("loads = %d, want 0", n)
	}
	if l.BoundDimension(context.Background()) != 0 {
		t.Error("a result that cannot say what its dimension is must not bind one")
	}
}

// A buyer who does know the dimension may say so, and then a result that
// disagrees is refused by the same rule — the check is against the column, so
// there is one rule and not two.
func TestConfiguredDimensionBindsAtMigrate(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Dimension: 8})
	ctx := context.Background()
	if got := l.BoundDimension(ctx); got != 8 {
		t.Fatalf("bound dimension = %d, want 8 straight after migrate", got)
	}
	if _, err := l.Load(ctx, smallResult(16), LoadOptions{}); err == nil {
		t.Fatal("a 16-dimensional result loaded into a schema declared as 8")
	}
}

// A result with no vectors is a normal result — DDL and graph sources produce
// them — and it must not bind anything, so that the first result that does
// have vectors is still free to decide.
func TestResultWithoutVectorsLeavesTheDimensionUnbound(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	res.Vectors = nil
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := l.BoundDimension(ctx); got != 0 {
		t.Errorf("bound dimension = %d, want 0", got)
	}
	if n := f.count(t, "chunks"); n != 2 {
		t.Errorf("chunks = %d, want 2: text without vectors is still worth storing", n)
	}
}

// A vector naming a chunk the result does not contain has nowhere to go. It is
// refused rather than dropped, because a store that silently holds fewer
// vectors than the result had is one whose recall is quietly wrong.
func TestVectorForAMissingChunkIsRefused(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: 99, Values: unit(8, 3), Model: "embed-4"})
	if _, err := l.Load(context.Background(), res, LoadOptions{}); err == nil {
		t.Fatal("a vector for chunk 99 of a two-chunk result was accepted")
	}
}
