package embed

import (
	"context"
	"testing"
)

// Everything at once, because these interact: blanks are dropped before
// batching, a failed batch removes a run of chunks from the middle, the batch
// size divides neither the corpus nor what survives it, and the calls finish
// out of order. Each of those shifts a position without shifting a chunk index,
// which is exactly how a vector ends up on its neighbour.
func TestAMixedCorpusPairsEveryVectorWithItsOwnChunk(t *testing.T) {
	texts := []string{
		"alpha body", "", "beta body", "gamma body", "   ", "delta body",
		"epsilon body", "zeta body", "\n\t", "eta body", "theta body",
	}
	chunks := testChunks(texts...)
	// Non-blank chunks in order are 0,2,3,5,6,7,9,10; in batches of 3 that is
	// (0,2,3) (5,6,7) (9,10), and the middle one fails.
	emb := &fakeEmbedder{errFor: map[string]error{chunks[6].Text: errEndpointDown}}

	for _, n := range []int{1, 3, 8} {
		got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 3, Concurrency: n})
		if err != nil {
			t.Fatalf("Embed at concurrency %d: %v", n, err)
		}
		if got.ChunksEmpty != 3 {
			t.Errorf("ChunksEmpty is %d, want 3", got.ChunksEmpty)
		}
		want := []int{0, 2, 3, 9, 10}
		if len(got.Vectors) != len(want) {
			t.Fatalf("got %d vectors, want %d: %+v", len(got.Vectors), len(want), got.Vectors)
		}
		for i, v := range got.Vectors {
			if v.Chunk != want[i] {
				t.Fatalf("vector %d is on chunk %d, want %d", i, v.Chunk, want[i])
			}
			if !equalVec(v.Values, wantVector(chunks[v.Chunk].Text)) {
				t.Errorf("vector on chunk %d embeds someone else's text", v.Chunk)
			}
		}
		// Nothing falls off the edge: every chunk is a vector, an unread, or a blank.
		if n := len(got.Vectors) + len(got.Unread) + got.ChunksEmpty; n != len(chunks) {
			t.Errorf("%d chunks accounted for, want %d", n, len(chunks))
		}
	}
}
