package embed

import (
	"context"
	"testing"
)

// The failure this guards against is silent: a vector on the wrong chunk is
// not wrong until months later, when a search returns a passage that has
// nothing to do with the query and nobody can say why. The batch size divides
// the corpus unevenly on purpose — 7 chunks in batches of 3 is 3+3+1 — because
// an off-by-one that survives a tidy 2x4 shows up on the ragged last batch.
func TestVectorsIndexTheirOwnChunk(t *testing.T) {
	chunks := testChunks(bodies(7)...)
	emb := &fakeEmbedder{name: "text-embedding-fake"}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 3})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got.Vectors) != len(chunks) {
		t.Fatalf("got %d vectors for %d chunks", len(got.Vectors), len(chunks))
	}
	for i, v := range got.Vectors {
		if v.Chunk != chunks[i].Index {
			t.Errorf("vector %d is attached to chunk %d, want %d", i, v.Chunk, chunks[i].Index)
		}
		want := wantVector(chunks[i].Text)
		if !equalVec(v.Values, want) {
			t.Errorf("vector for chunk %d is %v, want %v (the vector of chunk %d's text)",
				v.Chunk, v.Values, want, chunks[i].Index)
		}
		if v.Model != emb.Name() {
			t.Errorf("vector for chunk %d says model %q, want %q", v.Chunk, v.Model, emb.Name())
		}
	}
}

func equalVec(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
