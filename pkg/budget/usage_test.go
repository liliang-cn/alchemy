package budget_test

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
)

// usageEmbedder is the optional interface pkg/embed asks an embedder for when
// it wants to report what a call cost. It is restated here rather than
// imported because Go interfaces are structural and pkg/budget must not depend
// on a stage that uses it — but the method set has to match exactly, which is
// what this test is really checking.
type usageEmbedder interface {
	alchemy.Embedder
	EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error)
}

type reportingEmbedder struct{ calls int }

func (e *reportingEmbedder) Name() string { return "reports-usage" }

func (e *reportingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	v, _, err := e.EmbedUsage(ctx, texts)
	return v, err
}

func (e *reportingEmbedder) EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) {
	e.calls++
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 2, 3}
	}
	return out, 7 * len(texts), nil
}

type silentEmbedder struct{}

func (silentEmbedder) Name() string { return "reports-nothing" }
func (silentEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1}
	}
	return out, nil
}

// §7.2 says cost is never hidden, and a decorator is the easiest place in the
// system to hide it: the wrapper satisfies alchemy.Embedder, so everything
// compiles, and the usage an endpoint reported is dropped on the floor with
// nothing to notice it. A real run against a gateway that reports tokens is
// what surfaced this — the same job showed 258 tokens unwrapped and 0 wrapped.
//
// Wrapping must not change what a model is able to tell the caller. It may
// only change when the call happens.
func TestWrappingAnEmbedderDoesNotLoseTheUsageItReports(t *testing.T) {
	b, err := budget.NewLocal(budget.Config{Limit: 2})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	inner := &reportingEmbedder{}
	wrapped := budget.WrapEmbedder(inner, b)

	u, ok := wrapped.(usageEmbedder)
	if !ok {
		t.Fatalf("a wrapped %T no longer reports usage: the budget hid the bill", inner)
	}
	vecs, tokens, err := u.EmbedUsage(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedUsage: %v", err)
	}
	if len(vecs) != 3 {
		t.Errorf("vectors = %d, want 3", len(vecs))
	}
	if tokens != 21 {
		t.Errorf("tokens = %d, want 21 — the number the endpoint reported", tokens)
	}
	if inner.calls != 1 {
		t.Errorf("inner calls = %d, want exactly 1: a wrapper may pace a call, never repeat it", inner.calls)
	}
}

// The other half: an embedder that reports nothing must not be made to look as
// though it does. A wrapper that always claimed the interface would turn "this
// provider is silent" into "this provider says zero", and alchemy.ModelCall
// documents 0 as the first, not the second.
func TestWrappingASilentEmbedderDoesNotInventUsage(t *testing.T) {
	b, err := budget.NewLocal(budget.Config{Limit: 2})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	wrapped := budget.WrapEmbedder(silentEmbedder{}, b)
	if _, ok := wrapped.(usageEmbedder); ok {
		t.Fatal("a wrapped silent embedder claims to report usage; a caller would read its silence as zero")
	}
	if _, err := wrapped.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
}
