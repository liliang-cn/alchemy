package embed_test

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/embed"
)

// countingEmbedder reports usage the way a real gateway does.
type countingEmbedder struct{ dims int }

func (countingEmbedder) Name() string { return "reports-usage" }

func (e countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	v, _, err := e.EmbedUsage(ctx, texts)
	return v, err
}

func (e countingEmbedder) EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, e.dims)
	}
	return out, 11 * len(texts), nil
}

// This is the seam a real run broke, and it is worth a test that crosses it
// rather than two tests that each stop at their own edge.
//
// pkg/embed asks an embedder for its usage through an optional interface.
// pkg/budget wraps an embedder to pace it. Both were right about their own
// half: the stage reported what it was told, the wrapper passed a call through
// unchanged. What nothing tested was the composition — and the composition
// silently reported no tokens at all, because the wrapper satisfied the port
// without carrying the optional method.
//
// It surfaced only against a gateway that reports usage, on a job that had
// already been through every unit test in the repo. §7.2's promise is that a
// job's spend is never hidden, and it is the one promise a decorator can break
// while every assertion on either side of it still passes.
func TestUsageSurvivesBeingPacedByABudget(t *testing.T) {
	b, err := budget.NewLocal(budget.Config{Limit: 2})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	chunks := []alchemy.Chunk{
		{Index: 0, Source: "a.md", Text: "one"},
		{Index: 1, Source: "a.md", Text: "two"},
		{Index: 2, Source: "a.md", Text: "three"},
	}

	bare, err := embed.Embed(context.Background(), chunks, embed.Options{Embedder: countingEmbedder{dims: 4}})
	if err != nil {
		t.Fatalf("unwrapped: %v", err)
	}
	paced, err := embed.Embed(context.Background(), chunks, embed.Options{
		Embedder: budget.WrapEmbedder(countingEmbedder{dims: 4}, b),
	})
	if err != nil {
		t.Fatalf("wrapped: %v", err)
	}

	if len(bare.ModelCalls) != 1 || len(paced.ModelCalls) != 1 {
		t.Fatalf("model calls: unwrapped %+v, wrapped %+v", bare.ModelCalls, paced.ModelCalls)
	}
	if bare.ModelCalls[0].Tokens == 0 {
		t.Fatal("the control reported no tokens, so the comparison below proves nothing")
	}
	if paced.ModelCalls[0].Tokens != bare.ModelCalls[0].Tokens {
		t.Errorf("pacing changed the reported spend: %d wrapped vs %d unwrapped — a budget may decide when a call happens, never what it cost",
			paced.ModelCalls[0].Tokens, bare.ModelCalls[0].Tokens)
	}
	if len(paced.Vectors) != len(bare.Vectors) {
		t.Errorf("vectors: %d wrapped vs %d unwrapped", len(paced.Vectors), len(bare.Vectors))
	}
}
