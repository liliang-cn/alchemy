package pgvector

import (
	"context"
	"errors"
	"testing"
)

// The point of putting the graph and the vectors in one store is that "which
// text is about this" and "what did we extract from that text" are one join.
// This is that, end to end.
func TestSearchThenTheGraphAroundWhatItFound(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	loaded, err := l.Load(ctx, smallResult(8), LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	hits, err := l.Search(ctx, unit(8, 0), 2, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	near := hits[0]
	if near.Chunk != 0 || near.Distance > 1e-6 {
		t.Errorf("nearest = %+v, want chunk 0 at distance 0", near)
	}
	if near.Load != loaded.ID || near.Source != "architecture.pdf" || near.Heading != "Overview" {
		t.Errorf("hit = %+v; a hit has to say which load and which part of which file it is", near)
	}
	if near.Text == "" || near.Model != "embed-4" {
		t.Errorf("hit = %+v, want the text and the embedding model that produced the vector", near)
	}

	// Depth 0 is what the chunk itself produced.
	around, err := l.Around(ctx, hits[:1], 0)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	g := around[loaded.ID]
	if len(g.Entities) != 1 || g.Entities[0].ID != "SuperAI" || len(g.Relations) != 0 {
		t.Fatalf("depth 0 = %+v, want just SuperAI", g)
	}

	// Depth 1 is that plus what it connects to, in either direction.
	around, err = l.Around(ctx, hits[:1], 1)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	g = around[loaded.ID]
	if len(g.Relations) != 1 || g.Relations[0].Type != "USES" {
		t.Fatalf("depth 1 relations = %+v, want the USES edge", g.Relations)
	}
	if len(g.Entities) != 2 {
		t.Fatalf("depth 1 entities = %+v, want SuperAI and CortexDB", g.Entities)
	}
	// The edge still names its producer once it has been through the store,
	// which is the whole of §5b arriving at the place a buyer reads it.
	if p := g.Relations[0].Provenance; p.Producer != "llm-extract" || p.Model == "" || p.RuleSet == "" {
		t.Errorf("edge provenance after a round trip = %+v", p)
	}
}

// An edge is followed backwards as well as forwards. An agent asking what
// surrounds a node does not care which way the extractor wrote it, and a depth
// that meant "outgoing only" would make the same node's neighbourhood depend on
// which chunk happened to mention it first.
func TestAroundFollowsEdgesInBothDirections(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	loaded, err := l.Load(ctx, smallResult(8), LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Chunk 1 produced CortexDB, which is only ever the target of the edge.
	hits, err := l.Search(ctx, unit(8, 1), 1, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	around, err := l.Around(ctx, hits, 1)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	g := around[loaded.ID]
	var sawSuperAI bool
	for _, e := range g.Entities {
		sawSuperAI = sawSuperAI || e.ID == "SuperAI"
	}
	if !sawSuperAI {
		t.Errorf("around CortexDB = %+v, want it to reach SuperAI through the incoming edge", g.Entities)
	}
}

// Two loads are two graphs, and a search that crossed both says so instead of
// handing back one merged answer. It is the same refusal the fingerprint makes
// on the way in, and it has to hold on the way out or the refusal was cosmetic.
func TestAroundKeepsTwoLoadsApart(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	a, err := l.Load(ctx, smallResult(8), LoadOptions{})
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	second := smallResult(8)
	second.Entities[0].Name = "SuperAI (v2)"
	b, err := l.Load(ctx, second, LoadOptions{})
	if err != nil {
		t.Fatalf("load b: %v", err)
	}

	hits, err := l.Search(ctx, unit(8, 0), 4, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	around, err := l.Around(ctx, hits, 1)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	if len(around) != 2 {
		t.Fatalf("around covers %d loads, want 2", len(around))
	}
	names := map[string]string{}
	for load, g := range around {
		for _, e := range g.Entities {
			if e.ID == "SuperAI" {
				names[load] = e.Name
			}
		}
	}
	if names[a.ID] != "SuperAI" || names[b.ID] != "SuperAI (v2)" {
		t.Errorf("SuperAI per load = %v; the two runs were merged", names)
	}

	// And a caller who wants one of them says so.
	only, err := l.Search(ctx, unit(8, 0), 4, SearchOptions{Loads: []string{b.ID}})
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	for _, h := range only {
		if h.Load != b.ID {
			t.Errorf("scoped search returned a hit from %s", h.Load)
		}
	}
	if len(only) != 2 {
		t.Errorf("scoped search returned %d hits, want the 2 chunks of one load", len(only))
	}
}

// A query embedded by a different model than the corpus is a wrong answer
// waiting to happen, and it is refused in the buyer's terms rather than as a
// type error from the server.
func TestSearchRefusesAQueryOfTheWrongWidth(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err := l.Search(ctx, unit(16, 0), 1, SearchOptions{})
	var de *DimensionError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DimensionError", err)
	}
	if _, err := l.Search(ctx, nil, 1, SearchOptions{}); err == nil {
		t.Error("an empty query vector was accepted")
	}
	if _, err := l.Search(ctx, unit(8, 0), 0, SearchOptions{}); err == nil {
		t.Error("k = 0 was accepted")
	}
}

// A chunk nobody embedded keeps its text and is simply not searchable. §5c puts
// embedding after review, so a chunk that did not survive it legitimately has
// no vector, and dropping the text would lose what the reviewer was reading.
func TestAChunkWithNoVectorKeepsItsText(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	res.Vectors = res.Vectors[:1]
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := f.count(t, "chunks"); n != 2 {
		t.Errorf("chunks = %d, want 2", n)
	}
	hits, err := l.Search(ctx, unit(8, 0), 5, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("hits = %d, want 1: a chunk with no vector is not a search result", len(hits))
	}
	var body string
	f.scalar(t, &body, `SELECT body FROM {s}.loaded_chunks WHERE idx = 1`)
	if body == "" {
		t.Error("the unembedded chunk lost its text")
	}
}
