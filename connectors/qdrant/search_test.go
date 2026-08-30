package qdrant

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The first of the three questions this connector exists to make answerable:
// which text is about this. A hit carries what was extracted from it, because
// a store with no joins that answered with an id would make the next question
// a second round trip per result.
func TestSearchFindsTheNearestChunkAndSaysWhatCameOutOfIt(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	hits, err := l.Search(ctx, unit(8, 1), 2, Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].Chunk != 1 {
		t.Errorf("nearest chunk = %d, want 1", hits[0].Chunk)
	}
	if hits[0].Text != "SuperAI uses CortexDB." || hits[0].Source != "architecture.pdf" {
		t.Errorf("hit = %+v, want the text and the file it came from", hits[0])
	}
	if hits[0].Model != "embed-4" {
		t.Errorf("hit model = %q, want embed-4: which model embedded this is per-chunk in alchemy.Result", hits[0].Model)
	}
	if len(hits[0].EntityIDs) != 1 || hits[0].EntityIDs[0] != "CortexDB" {
		t.Errorf("hit entities = %v, want [CortexDB] carried on the hit itself", hits[0].EntityIDs)
	}

	// Provenance filters apply to a search too, which is the combination a
	// payload store is good at: nearest neighbours among the records a person
	// has already accepted, or among the ones from one file.
	if hits, err := l.Search(ctx, unit(8, 1), 5, Filter{Source: "nowhere.pdf"}); err != nil || len(hits) != 0 {
		t.Errorf("search filtered to a file that is not there = %d hits (err %v), want 0", len(hits), err)
	}
}

// A query embedding from a different model than the corpus is a wrong answer
// waiting to happen rather than a type error, so it is refused in the buyer's
// terms and not the server's.
func TestSearchingWithTheWrongWidthIsRefused(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err := l.Search(ctx, unit(16, 1), 3, Filter{})
	var de *DimensionError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DimensionError", err)
	}
	if de.Have != 8 || de.Want != 16 {
		t.Errorf("DimensionError = have %d want %d, want have 8 want 16", de.Have, de.Want)
	}
}

// The second question, and the reason to put a graph and its vectors in one
// store: what did we extract from the text a search found, and what does it
// connect to. In a graph database this is a traversal; here it is a filtered
// lookup per hop, which is the trade this connector makes and states.
func TestAroundReturnsTheGraphThatSurroundsAHit(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, smallResult(8), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	hits, err := l.Search(ctx, unit(8, 1), 1, Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// Depth 0 is what the hit itself produced: only CortexDB came out of
	// chunk 1.
	zero, err := l.Around(ctx, hits, 0)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	if len(zero) != 1 {
		t.Fatalf("Around returned %d loads, want 1", len(zero))
	}
	for _, g := range zero {
		if len(g.Entities) != 1 || g.Entities[0].ID != "CortexDB" || len(g.Relations) != 0 {
			t.Errorf("depth 0 = %+v, want CortexDB and no edges", g)
		}
	}

	one, err := l.Around(ctx, hits, 1)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	for load, g := range one {
		if load != hits[0].Load {
			t.Errorf("Around keyed the graph by %q, want the hit's load %q", load, hits[0].Load)
		}
		if len(g.Relations) != 1 || g.Relations[0].From != "SuperAI" {
			t.Fatalf("depth 1 relations = %+v, want the USES edge", g.Relations)
		}
		// The edge was found from its target, which is the direction the
		// extractor did not write it in — an agent asking what surrounds a node
		// does not care which way round the edge was recorded.
		ids := map[string]bool{}
		for _, e := range g.Entities {
			ids[e.ID] = true
		}
		if !ids["SuperAI"] || !ids["CortexDB"] || len(g.Entities) != 2 {
			t.Errorf("depth 1 entities = %v, want both ends of the edge", ids)
		}
		// And the edge is readable on its own, which is what a store with no
		// joins has to buy by denormalising.
		if g.Relations[0].Attributes["since"] != "2025" {
			t.Errorf("edge attributes = %+v, want the source's own words", g.Relations[0].Attributes)
		}
	}
}

// A retirement is stored, is readable, and takes nothing with it.
//
// The last clause is what the test is for. alchemy states that a record is over
// and never performs the retirement, and this connector is a delete-by-filter
// away from performing it — so what is asserted is that the entity named in
// `retires` is still a point afterwards, and that the claim is beside it rather
// than applied to it. The second retirement names a record no load in this
// collection has ever held, which is the ordinary case for the field and must
// be stored exactly like the first.
func TestARetirementIsStoredBesideTheGraphAndNotAppliedToIt(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})

	res := smallResult(4)
	who := alchemy.Provenance{
		Source: "correction.md", Chunk: -1, Producer: alchemy.ProducerHuman,
		By: "ana@example.com", At: "2026-03-01T00:00:00Z",
	}
	res.Supersessions = []alchemy.Supersession{
		{Retires: "CortexDB", By: alchemy.Ref{Kind: alchemy.RefEntity, ID: "SuperAI", Type: "Service"},
			Reason: "the store was replaced", Provenance: who},
		{Retires: "e-from-last-month", By: alchemy.Ref{Kind: alchemy.RefEntity, ID: "SuperAI", Type: "Service"},
			Reason: "the old profile is stale", Provenance: who},
	}

	out, err := l.Load(context.Background(), res, LoadOptions{ID: "ld-1"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Supersessions != 2 {
		t.Fatalf("Loaded.Supersessions = %d, want both", out.Supersessions)
	}

	recs, err := l.Records(context.Background(), Filter{Loads: []string{"ld-1"}}, 0)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs.Supersessions) != 2 {
		t.Fatalf("%d retirements read back, want 2: a store that writes a point no reader returns holds "+
			"a record nobody can see", len(recs.Supersessions))
	}
	// The entity the first one retires is still here, untouched.
	if len(recs.Entities) != 2 {
		t.Fatalf("%d entities after two retirements, want 2: alchemy states a retirement and never performs one",
			len(recs.Entities))
	}
	// Found by what it retires rather than taken by position. Records sorts
	// these by Retires and the order is a real guarantee, but an index here
	// would be a test asserting the collation: "CortexDB" sorts before
	// "e-from-last-month" because C is 0x43 and e is 0x65, which is true, easy
	// to get backwards, and not the property this test is about.
	//
	// This is the one naming a record no load here holds — stored like any
	// other, because that is the case the field exists for.
	var got alchemy.Supersession
	for _, s := range recs.Supersessions {
		if s.Retires == "e-from-last-month" {
			got = s
		}
	}
	if got.Retires != "e-from-last-month" || got.By.ID != "SuperAI" {
		t.Errorf("retirement = %+v, want what it retires and what replaces it; read back %+v",
			got, recs.Supersessions)
	}
	if got.Provenance.By != "ana@example.com" {
		t.Errorf("prov By = %q, want the person whose word this is: a retirement nobody can attribute is "+
			"a deletion with a nicer name", got.Provenance.By)
	}
}
