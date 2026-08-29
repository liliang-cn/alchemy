package pipeline

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// §3's last stage. The vectors are of the chunks the job actually carries, and
// each one names the chunk it belongs to by the job-wide index — two sources
// numbering their own chunks from zero would otherwise leave a vector naming
// two different pages.
func TestChunksAreEmbeddedAfterEverythingElse(t *testing.T) {
	emb := &fakeEmbedder{name: "fake-embed-3"}
	req := regionRequest(t, doc("eu.md", docEU))
	req.Models.Embedder = emb
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("no chunks")
	}
	if len(res.Vectors) != len(res.Chunks) {
		t.Fatalf("%d vectors for %d chunks", len(res.Vectors), len(res.Chunks))
	}
	byChunk := map[int]alchemy.Vector{}
	for _, v := range res.Vectors {
		if v.Model != "fake-embed-3" {
			t.Errorf("vector model = %q, want the embedder that produced it", v.Model)
		}
		byChunk[v.Chunk] = v
	}
	for _, c := range res.Chunks {
		if _, ok := byChunk[c.Index]; !ok {
			t.Errorf("chunk %d has no vector", c.Index)
		}
	}
	if got := len(emb.embedded()); got != len(res.Chunks) {
		t.Errorf("the embedder was given %d texts for %d chunks", got, len(res.Chunks))
	}
}

// A job with no embedder is not an error: pkg/embed documents a nil Embedder
// as "no vectors were asked for", and a caller who wants a graph and no
// vectors should not have to supply a model to be refused by.
func TestNoEmbedderMeansNoVectorsRatherThanAFailure(t *testing.T) {
	res, err := Run(context.Background(), regionRequest(t, doc("eu.md", docEU)), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Vectors) != 0 {
		t.Errorf("want no vectors without an embedder, got %d", len(res.Vectors))
	}
}
