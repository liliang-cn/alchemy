package cache_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
)

func newShared(t *testing.T, pool *pgxpool.Pool, cfg cache.PostgresConfig) cache.Cache {
	t.Helper()
	c, err := cache.NewPostgres(context.Background(), pool, cfg)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	return c
}

// richEntry is what a real extraction returns, with every value the declared
// attribute domain allows and provenance on every entity and every relation.
// §5b makes provenance a product guarantee: a cache that loses it turns an
// attributable edge into an anonymous one, so a resumed job would produce a
// graph that is less explainable than a fresh one and nothing would say so.
func richEntry() cache.Entry {
	prov := alchemy.Provenance{
		Source:     "architecture.pdf",
		Chunk:      14,
		Producer:   alchemy.ProducerLLMExtract,
		Model:      "gemini-3.6-flash-high",
		Ontology:   "sds@3",
		Chunking:   "heading",
		Confidence: 0.82,
		ReviewedBy: "ll@example.com",
		Rules:      "System USES System; always",
	}
	// A second provenance that differs in every field, so a store that filled
	// one row's provenance in from another's would be caught.
	other := alchemy.Provenance{
		Source:   "schema.sql",
		Chunk:    -1,
		Producer: alchemy.ProducerDDL,
	}
	return cache.Entry{
		Entities: []alchemy.Entity{
			{
				ID: "e1", Type: "System", Name: "SuperAI",
				Attributes: map[string]any{
					"lang":   "go",
					"public": true,
					"replic": float64(3),
					"ratio":  0.125,
					// Values chosen to break a store that goes through a
					// decimal type on the way: jsonb holds numbers as numeric,
					// and a round trip that loses the last bit of a float64
					// would change an answer without changing a count.
					"awkward": 0.1 + 0.2,
					"tiny":    1e-300,
					"huge":    1.7976931348623157e+308,
					"unknown": nil,
					"tags":    []any{"a", float64(2), false, nil},
					"nested":  map[string]any{"k": "v", "deep": []any{map[string]any{"x": true}}},
				},
				Provenance: prov,
			},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: other},
			// No attributes at all: nil must come back nil, not an empty map,
			// because the two serialise differently and the JSON is the
			// contract (§4).
			{ID: "e3", Type: "Store", Name: "Postgres", Provenance: prov},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Attributes: map[string]any{"since": "2025"}, Provenance: prov},
			{From: "e2", To: "e3", Type: "STORES_IN", Provenance: other},
		},
		Tokens: 1731,
	}
}

// The test the whole wire-format decision exists for. One node writes, another
// node reads, and what comes back is identical — not equal-ish, identical,
// including the Go type of every attribute value and the provenance of every
// entity and relation.
func TestACachedEntryRoundTripsIdenticallyBetweenNodes(t *testing.T) {
	pg := newPG(t)
	writer := newShared(t, pg.pool(false), cache.PostgresConfig{})
	reader := newShared(t, pg.pool(false), cache.PostgresConfig{})
	ctx := context.Background()

	want := richEntry()
	if err := writer.Put(ctx, base(), want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := reader.Get(ctx, base())
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the entry changed crossing the store:\n got %#v\nwant %#v", got, want)
	}
	assertEntryEqual(t, got, want)

	// DeepEqual would already catch a type change, but the failure it prints
	// does not say "int became float64", and that is the sentence a person
	// needs. So the types are named.
	for key, wv := range want.Entities[0].Attributes {
		gv := got.Entities[0].Attributes[key]
		if fmt.Sprintf("%T", gv) != fmt.Sprintf("%T", wv) {
			t.Errorf("attribute %q came back as %T, was stored as %T", key, gv, wv)
		}
	}
	if got.Entities[1].Attributes != nil {
		t.Errorf("an entity that stated no attributes came back with %v", got.Entities[1].Attributes)
	}
}

// A hit must not be a write. LRU across nodes would need one — Get would have
// to update a recency column — and this is the test that says the shared store
// does not do that: the reader's connection refuses every write, and the hit
// still works.
func TestAHitDoesNotWriteToTheStore(t *testing.T) {
	pg := newPG(t)
	writer := newShared(t, pg.pool(false), cache.PostgresConfig{})
	if err := writer.Put(context.Background(), base(), richEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reader, err := cache.NewPostgres(context.Background(), pg.pool(true), cache.PostgresConfig{})
	if err != nil {
		t.Fatalf("NewPostgres on a read-only connection: %v", err)
	}
	got, ok, err := reader.Get(context.Background(), base())
	if err != nil {
		t.Fatalf("a cache hit tried to write: %v", err)
	}
	if !ok {
		t.Fatal("miss on a key another node stored")
	}
	assertEntryEqual(t, got, richEntry())
}

func TestASharedMissIsNotAnError(t *testing.T) {
	pg := newPG(t)
	c := newShared(t, pg.pool(false), cache.PostgresConfig{})
	got, ok, err := c.Get(context.Background(), base())
	if err != nil {
		t.Fatalf("miss returned an error: %v", err)
	}
	if ok {
		t.Fatal("miss reported a hit")
	}
	if len(got.Entities) != 0 || got.Tokens != 0 {
		t.Fatalf("miss returned a non-zero entry: %+v", got)
	}
}

// §8.3: a lease that expires because a node was merely slow means two nodes
// briefly work the same job, and the second writer must lose harmlessly.
func TestTheSecondWriterOfAnAddressLosesHarmlessly(t *testing.T) {
	pg := newPG(t)
	a := newShared(t, pg.pool(false), cache.PostgresConfig{})
	b := newShared(t, pg.pool(false), cache.PostgresConfig{})
	ctx := context.Background()

	if err := a.Put(ctx, base(), richEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Put(ctx, base(), richEntry()); err != nil {
		t.Fatalf("the second writer failed: %v", err)
	}
	got, ok, err := a.Get(ctx, base())
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	assertEntryEqual(t, got, richEntry())
}

// The shared store enforces the same attribute domain as the in-process one:
// one contract, two implementations.
func TestTheSharedStoreRefusesTheSameValuesTheLocalOneDoes(t *testing.T) {
	pg := newPG(t)
	c := newShared(t, pg.pool(false), cache.PostgresConfig{})
	err := c.Put(context.Background(), base(), cache.Entry{
		Entities: []alchemy.Entity{{ID: "e1", Attributes: map[string]any{"n": 1}}},
	})
	if !errors.Is(err, cache.ErrUnsupportedAttribute) {
		t.Fatalf("Put = %v, want ErrUnsupportedAttribute", err)
	}
}

// Eviction across nodes is by age, not by recency. An entry past MaxAge is not
// served even before a sweep has run, so what is returned never depends on when
// somebody last ran a sweep.
func TestAnEntryPastItsAgeIsNeitherServedNorKept(t *testing.T) {
	pg := newPG(t)
	pool := pg.pool(false)
	c := newShared(t, pool, cache.PostgresConfig{MaxAge: 300 * time.Millisecond})
	ctx := context.Background()

	if err := c.Put(ctx, base(), richEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok, _ := c.Get(ctx, base()); !ok {
		t.Fatal("miss on a fresh entry")
	}
	time.Sleep(400 * time.Millisecond)
	if _, ok, _ := c.Get(ctx, base()); ok {
		t.Fatal("an entry past MaxAge was served: eviction would depend on when a sweep last ran")
	}

	swept, err := c.(*cache.Postgres).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("Sweep removed %d rows, want 1", swept)
	}
}

func TestSweepHonoursTheEntryCap(t *testing.T) {
	pg := newPG(t)
	c := newShared(t, pg.pool(false), cache.PostgresConfig{MaxEntries: 3})
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		k := base()
		k.Chunk = fmt.Sprintf("chunk %d", i)
		if err := c.Put(ctx, k, richEntry()); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if _, err := c.(*cache.Postgres).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	kept := 0
	for i := 0; i < 6; i++ {
		k := base()
		k.Chunk = fmt.Sprintf("chunk %d", i)
		if _, ok, _ := c.Get(ctx, k); ok {
			kept++
		}
	}
	if kept != 3 {
		t.Fatalf("%d entries survived a cap of 3", kept)
	}
}

// The two-node miss race, measured rather than fixed. Both nodes miss, both buy
// the same call, and both answers are the same because the address says so.
// What is lost is the saving, not the correctness, and the test states the size
// of the loss: one duplicate call per racing pair per address.
func TestTwoNodesMissingTogetherBothBuyTheCall(t *testing.T) {
	pg := newPG(t)
	nodes := []cache.Cache{
		newShared(t, pg.pool(false), cache.PostgresConfig{}),
		newShared(t, pg.pool(false), cache.PostgresConfig{}),
	}
	var bought atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup

	for _, c := range nodes {
		wg.Add(1)
		go func(c cache.Cache) {
			defer wg.Done()
			<-start
			_, hit, err := cache.Fetch(context.Background(), c, base(), func(context.Context) (cache.Entry, error) {
				bought.Add(1)
				time.Sleep(50 * time.Millisecond) // the model call both nodes pay for
				return richEntry(), nil
			})
			if err != nil {
				t.Errorf("Fetch: %v", err)
			}
			_ = hit
		}(c)
	}
	close(start)
	wg.Wait()

	if got := bought.Load(); got != 2 {
		t.Fatalf("the racing nodes bought the call %d times, want 2: this test records the cost, it does not prevent it", got)
	}
	// And the third reader pays nothing, which is the part that matters.
	_, hit, err := cache.Fetch(context.Background(), nodes[0], base(), func(context.Context) (cache.Entry, error) {
		t.Error("a cached address was bought again")
		return cache.Entry{}, nil
	})
	if err != nil || !hit {
		t.Fatalf("Fetch after the race: hit=%v err=%v", hit, err)
	}
}
