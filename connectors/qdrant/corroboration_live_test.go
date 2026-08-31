package qdrant

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/sinkconform"
)

// The graph this product exists to produce, loaded through the connector's own
// Load rather than through the envelope.
//
// This is where the defect was, and where the shared suite could not see it.
// sinkconform drives sink.Load directly -- that is the whole point of it, since
// §4.1 put the envelope above the line -- so it never calls this package's Load,
// and this package's Load is where checkEntityIDs stood. Two documents each
// asserting the same node were refused before a row was written, by a rule that
// was correct until pkg/preflight legalised corroboration and was never
// revisited. Every test that existed went through the envelope and passed.
func TestAGraphTwoSourcesAgreeAboutLoads(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()

	res := sinkconform.Corroborated(0)
	got, err := l.Load(ctx, res, LoadOptions{ID: "corroborated"})
	if err != nil {
		t.Fatalf("Load of a node two sources agree about: %v; "+
			"a graph merged from more than one document is the ordinary case, not a broken result", err)
	}
	// Loaded.Entities is what the RESULT held, which is three records under two
	// IDs. The store holding two rows is sink.Report's business and the shared
	// suite's; what this asserts is that the load happened at all.
	if got.Entities != len(res.Entities) {
		t.Errorf("Entities = %d, want %d", got.Entities, len(res.Entities))
	}

	// And the rule did not widen into nothing: two records under one ID that
	// describe different nodes are still refused, still with this package's own
	// typed error, because a caller matching on it is matching on a contract.
	collided := sinkconform.Corroborated(0)
	collided.Entities[2].Name = "Something Else Entirely"
	collided.Counts = collided.Derivable()
	_, err = l.Load(ctx, collided, LoadOptions{ID: "collided"})
	var dup *DuplicateEntityError
	if !errors.As(err, &dup) {
		t.Fatalf("Load of two different nodes under one ID = %v, want *DuplicateEntityError", err)
	}
	if dup.ID != "e2" {
		t.Errorf("the error names %q, want the ID that collided", dup.ID)
	}
}
