package budget

import (
	"context"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The decorators are how a stage gets budgeted without knowing that budgets
// exist. pkg/extract asks for an alchemy.LLM and calls Complete; whether that
// LLM waits for a cluster-wide slot first is the assembler's business, not the
// stage's. The alternative — every stage taking a Budget parameter — spreads
// one deployment concern through every file that talks to a model.
//
// Three properties hold for all three wrappers:
//
//   - Name is passed through unchanged. Provenance and alchemy.ModelCall are
//     keyed on it, so a wrapper that renamed the model would corrupt every
//     citation in the result and split the cost report in two.
//   - Nothing is retried here. One call in is one call out, because §7.2 makes
//     the job's own count of model calls a number the caller is owed, and a
//     wrapper that silently called twice would make it wrong. What the wrapper
//     does instead is report the rate limit to the budget, so that whoever does
//     retry is held at Acquire until the endpoint is open again — the retry is
//     paced by the same lease rather than by each node's own guess (§8.2).
//   - The slot is released on every path out, including a panic. A model that
//     panics is a bug that costs one call; a model that panics and leaks a slot
//     is a budget that shrinks to zero and a node that stops working.

// WrapLLM returns an alchemy.LLM that holds a budget slot for the duration of
// each Complete.
func WrapLLM(inner alchemy.LLM, b Budget) alchemy.LLM {
	return &budgetedLLM{inner: inner, budget: b}
}

// WrapEmbedder returns an alchemy.Embedder that holds a budget slot for the
// duration of each Embed. Batching stays the implementation's business: the
// budget bounds calls, and one Embed is one call however many texts it carries.
func WrapEmbedder(inner alchemy.Embedder, b Budget) alchemy.Embedder {
	return &budgetedEmbedder{inner: inner, budget: b}
}

// WrapOCR returns an alchemy.OCR that holds a budget slot for the duration of
// each Recognize.
func WrapOCR(inner alchemy.OCR, b Budget) alchemy.OCR {
	return &budgetedOCR{inner: inner, budget: b}
}

type budgetedLLM struct {
	inner  alchemy.LLM
	budget Budget
}

func (m *budgetedLLM) Name() string { return m.inner.Name() }

func (m *budgetedLLM) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	lease, err := m.budget.Acquire(ctx, m.inner.Name())
	if err != nil {
		// No slot, so no call: a request that was never sent cannot be part of
		// the bill, and the caller gets the context error rather than a
		// response that looks like the model said nothing.
		return alchemy.LLMResponse{}, err
	}
	// outcome starts as "did not return" so that a panic unwinding through this
	// frame still frees the slot without being recorded as a healthy call —
	// which would reset a backoff the endpoint never recovered from. It is a
	// flag rather than a recover() so the caller's panic reaches them intact.
	outcome := errUnfinished
	defer func() { lease.Release(outcome) }()

	resp, err := m.inner.Complete(ctx, req)
	outcome = err
	// The response is returned exactly as it came back, Tokens included.
	return resp, err
}

type budgetedEmbedder struct {
	inner  alchemy.Embedder
	budget Budget
}

func (m *budgetedEmbedder) Name() string { return m.inner.Name() }

func (m *budgetedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	lease, err := m.budget.Acquire(ctx, m.inner.Name())
	if err != nil {
		return nil, err
	}
	outcome := errUnfinished
	defer func() { lease.Release(outcome) }()

	vecs, err := m.inner.Embed(ctx, texts)
	outcome = err
	return vecs, err
}

type budgetedOCR struct {
	inner  alchemy.OCR
	budget Budget
}

func (m *budgetedOCR) Name() string { return m.inner.Name() }

func (m *budgetedOCR) Recognize(ctx context.Context, page []byte, mediaType string) (string, error) {
	lease, err := m.budget.Acquire(ctx, m.inner.Name())
	if err != nil {
		return "", err
	}
	outcome := errUnfinished
	defer func() { lease.Release(outcome) }()

	text, err := m.inner.Recognize(ctx, page, mediaType)
	outcome = err
	return text, err
}
