package qdrant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A Qdrant collection fixes its vector size and its distance metric when it is
// created, and neither can be changed afterwards. The size is a job-time fact
// — alchemy.Vector.Values is whatever the caller's embedding model returned —
// so a connector has to either be told it or learn it, and being told it is
// the case that must work first.
func TestEnsureCollectionCreatesItAtTheConfiguredDimension(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{Dimension: 8})
	ctx := context.Background()
	if err := l.EnsureCollection(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	got, err := l.CollectionDimension(ctx)
	if err != nil {
		t.Fatalf("dimension: %v", err)
	}
	if got != 8 {
		t.Errorf("collection holds vector(%d), want vector(8)", got)
	}
	// Running it again is what every process that starts at once does, and it
	// must not be nine "already exists" crashes and one collection.
	if err := l.EnsureCollection(ctx); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
}

// A collection that already exists at another width cannot be widened: Qdrant
// has no ALTER. Truncating or padding the vectors would answer queries with
// embeddings that no longer mean what the model said, so the load is refused
// with both numbers and the model's name in it.
func TestACollectionAtAnotherDimensionIsRefusedInWords(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.openRaw(t, Config{Dimension: 8}).EnsureCollection(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	err := f.openRaw(t, Config{Dimension: 16}).EnsureCollection(ctx)
	var de *DimensionError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DimensionError", err)
	}
	if de.Have != 8 || de.Want != 16 {
		t.Errorf("DimensionError = have %d want %d, want have 8 want 16", de.Have, de.Want)
	}
	if !strings.Contains(err.Error(), "8") || !strings.Contains(err.Error(), "16") {
		t.Errorf("the refusal has to carry both numbers, got: %v", err)
	}
}
