package embed

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
)

// A provider that answers a batch of 40 with 39 vectors has misaligned every
// chunk after the one it dropped. Nothing downstream can detect that: the
// vectors are well-formed, the count is merely short, and search quality
// degrades without a single error anywhere. So it is refused here, loudly, with
// both numbers in the message — a run that returns 39 correct vectors and no
// complaint is the failure this whole package is arranged to prevent.
func TestShortBatchIsAHardErrorNamingTheMismatch(t *testing.T) {
	chunks := testChunks(bodies(10)...)
	emb := &fakeEmbedder{shortFor: map[string]bool{chunks[5].Text: true}}

	_, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4})
	if err == nil {
		t.Fatal("a provider returning too few vectors was accepted silently")
	}
	// The message has to carry the numbers: "the embedder misbehaved" sends a
	// reader to our code, "asked for 4, got 3" sends them to their provider.
	for _, want := range []string{"4", "3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name the mismatch (%q missing): %v", want, err)
		}
	}
}

// A provider answering with *more* vectors than texts is the same class of bug
// and is refused for the same reason: whatever the extra vector is, it is not
// an embedding of a chunk we sent.
func TestOverLongBatchIsAlsoRefused(t *testing.T) {
	chunks := testChunks(bodies(4)...)
	emb := &fakeEmbedder{longFor: map[string]bool{chunks[0].Text: true}}

	_, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4})
	if err == nil {
		t.Fatal("a provider returning too many vectors was accepted silently")
	}
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error does not name the count returned: %v", err)
	}
}

// The Embedder port takes a slice, so how big a slice is this package's
// decision and a caller who states nothing still gets a stated one.
func TestBatchSizeDefaultsAndBatchesAreWholeAndInOrder(t *testing.T) {
	n := DefaultBatchSize*2 + 5 // deliberately not a multiple.
	chunks := testChunks(bodies(n)...)
	emb := &fakeEmbedder{}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got.Vectors) != n {
		t.Fatalf("got %d vectors for %d chunks", len(got.Vectors), n)
	}
	batches := emb.sentBatches()
	if len(batches) != 3 {
		t.Fatalf("got %d batches for %d chunks at a default of %d, want 3",
			len(batches), n, DefaultBatchSize)
	}
	// Sizes, not order: which batch reaches the endpoint first is the
	// scheduler's business and this package promises nothing about it.
	sizes := []int{}
	for _, b := range batches {
		sizes = append(sizes, len(b))
	}
	sort.Ints(sizes)
	if want := []int{5, DefaultBatchSize, DefaultBatchSize}; !slices.Equal(sizes, want) {
		t.Errorf("batch sizes are %v, want %v", sizes, want)
	}
	// Every chunk's text reached the model exactly once. A batching bug that
	// drops or repeats a chunk is otherwise only visible as a vector count.
	seen := map[string]int{}
	for _, txt := range emb.sentTexts() {
		seen[txt]++
	}
	for _, c := range chunks {
		if seen[c.Text] != 1 {
			t.Errorf("chunk %d's text was sent %d times, want once", c.Index, seen[c.Text])
		}
	}
}
