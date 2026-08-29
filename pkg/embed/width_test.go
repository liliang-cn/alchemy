package embed

import (
	"context"
	"strings"
	"testing"
)

// One run's vectors have to share a width, and no index will accept them if
// they do not. A provider that answers 768 dimensions for one batch and 1536
// for the next has changed model underneath the job, and there is nothing in
// the data to say which half is the one the caller meant — the same shape of
// question as a short batch, and refused the same way, with both numbers in
// the message.
//
// Failing here rather than at insert time is the point: the alternative is a
// job that spends every call it was going to spend and is then rejected whole
// by the vector store, having reported success.
func TestVectorsOfTwoWidthsAreRefused(t *testing.T) {
	chunks := testChunks(bodies(6)...)
	emb := &fakeEmbedder{widthFor: map[string]int{chunks[4].Text: 7}}

	_, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 2})
	if err == nil {
		t.Fatal("a run whose vectors have two different widths reported success")
	}
	for _, want := range []string{"3", "7", "4"} { // the two widths, and the chunk.
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name the mismatch (%q missing): %v", want, err)
		}
	}
}

// A run where every vector has the same width is not disturbed by this check,
// including when a chunk in the middle has no vector at all.
func TestOneWidthThroughoutIsFine(t *testing.T) {
	chunks := testChunks(bodies(6)...)
	emb := &fakeEmbedder{emptyFor: map[string]bool{chunks[3].Text: true}}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got.Vectors) != 5 {
		t.Fatalf("got %d vectors, want 5", len(got.Vectors))
	}
}
