package pgvector

import (
	"context"
	"strings"
	"testing"
)

func indexDef(t *testing.T, f *fixture) string {
	t.Helper()
	var def string
	f.scalar(t, &def, `SELECT coalesce(string_agg(indexdef, ' | '), '')
		FROM pg_indexes WHERE schemaname = $1 AND tablename = 'chunks' AND indexdef LIKE '%embedding%'`, f.schema)
	return def
}

// An index is a decision, not a default. Building one at load time would mean
// every small import pays for a structure it cannot benefit from, and — for
// ivfflat — trains centroids on data that is not yet representative of the
// corpus, which is a recall problem that never announces itself.
func TestNoVectorIndexIsBuiltByALoad(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	if _, err := l.Load(context.Background(), smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if def := indexDef(t, f); def != "" {
		t.Errorf("a load built %q; the connector must not decide this for the buyer", def)
	}
}

// Asked on a table too small to benefit, it declines and says why. Declining is
// not an error: the caller asked a reasonable question and got a reasoned
// answer, and an error would push them towards ignoring it.
func TestASmallTableIsDeclinedWithItsNumbers(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	rep, err := l.EnsureVectorIndex(ctx, IndexOptions{})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if rep.Created {
		t.Error("an index was built over 2 rows")
	}
	if rep.Rows != 2 || !strings.Contains(rep.Reason, "2") {
		t.Errorf("report = %+v; the reason has to carry the numbers the decision was made on", rep)
	}
	if def := indexDef(t, f); def != "" {
		t.Errorf("index built anyway: %q", def)
	}
}

// When one is built, it is HNSW by default. HNSW needs no training data, so it
// is correct at any size; ivfflat's centroids come from whatever happened to be
// loaded first, which on a corpus loaded in job order is not a sample of the
// corpus at all.
func TestHNSWIsWhatGetsBuiltAndSearchStillFindsTheNearest(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500})
	ctx := context.Background()
	if _, err := l.Load(ctx, bigResult(2000, 16), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	rep, err := l.EnsureVectorIndex(ctx, IndexOptions{MinRows: 1})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !rep.Created || rep.Kind != IndexHNSW {
		t.Fatalf("report = %+v, want an hnsw index", rep)
	}
	def := indexDef(t, f)
	if !strings.Contains(def, "hnsw") || !strings.Contains(def, "vector_cosine_ops") {
		t.Errorf("index def = %q, want hnsw with vector_cosine_ops", def)
	}
	hits, err := l.Search(ctx, unit(16, 3), 1, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk%16 != 3 {
		t.Errorf("nearest = %+v, want a chunk whose vector is the query's", hits)
	}

	// Asking again is a no-op rather than a rebuild: an hour of index build is
	// not something a caller should trigger by calling a method twice.
	again, err := l.EnsureVectorIndex(ctx, IndexOptions{MinRows: 1})
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if again.Created {
		t.Error("the index was rebuilt")
	}
}

// ivfflat is offered, and its one real parameter is derived rather than left at
// a number nobody can justify. pgvector's own guidance is rows/1000 up to a
// million rows; a caller who knows better passes Lists.
func TestIvfflatListsAreDerivedFromTheRowCount(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500})
	ctx := context.Background()
	if _, err := l.Load(ctx, bigResult(3000, 16), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	rep, err := l.EnsureVectorIndex(ctx, IndexOptions{Kind: IndexIVFFlat, MinRows: 1})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !rep.Created || rep.Lists != 3 {
		t.Fatalf("report = %+v, want lists 3 for 3000 rows", rep)
	}
	if def := indexDef(t, f); !strings.Contains(def, "lists='3'") {
		t.Errorf("index def = %q, want lists='3'", def)
	}
}

// The index and the search have to agree about the metric, or the index is
// simply never used and the only symptom is that queries got slow.
func TestTheIndexIsBuiltForTheConfiguredDistance(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Batch: 500, Distance: L2})
	ctx := context.Background()
	if _, err := l.Load(ctx, bigResult(1200, 16), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := l.EnsureVectorIndex(ctx, IndexOptions{MinRows: 1}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if def := indexDef(t, f); !strings.Contains(def, "vector_l2_ops") {
		t.Errorf("index def = %q, want vector_l2_ops", def)
	}
}

// There is nothing to index before a dimension is bound, and saying so beats
// letting Postgres complain that it cannot index a column that does not exist.
func TestAnIndexCannotBeBuiltBeforeADimensionIsBound(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	_, err := l.EnsureVectorIndex(context.Background(), IndexOptions{MinRows: 1})
	if err == nil {
		t.Fatal("an index was built on a schema with no embeddings")
	}
	if !strings.Contains(err.Error(), "vector") {
		t.Errorf("err = %v; it should say there are no embeddings yet", err)
	}
}
