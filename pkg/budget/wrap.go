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
	m := &budgetedEmbedder{inner: inner, budget: b}
	// An embedder may know what its calls cost, and says so by carrying a
	// second method rather than by widening alchemy.Embedder — the usage a
	// provider reports is optional, and a port that demanded it would make
	// every silent provider lie about zero.
	//
	// That optionality is what makes this decorator dangerous. A wrapper that
	// implements only alchemy.Embedder still satisfies every caller and still
	// compiles, and the usage is simply gone: a real run against a gateway that
	// reports tokens showed 258 unwrapped and 0 wrapped, and nothing in between
	// could have noticed. §7.2 says cost is not optimised for and is never
	// hidden; a decorator that quietly drops the number hides it more
	// completely than any accounting bug, because the total still adds up.
	//
	// So the shape follows the inner model: a reporting embedder is wrapped in
	// something that reports, a silent one in something that stays silent. Go
	// has no conditional method set, which is why this is two types and a type
	// assertion rather than one type and an if.
	if u, ok := inner.(usageEmbedder); ok {
		return &budgetedUsageEmbedder{budgetedEmbedder: m, inner: u}
	}
	return m
}

// usageEmbedder is the optional interface an embedder carries when it knows
// what a call cost. It is declared here rather than imported because a budget
// must not depend on the stage that spends it; Go interfaces are structural, so
// an embedder satisfies both this and the stage's own declaration without
// either package knowing about the other.
type usageEmbedder interface {
	alchemy.Embedder
	EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error)
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

// budgetedUsageEmbedder is budgetedEmbedder for an inner model that reports
// usage. It exists only to carry the extra method; the pacing is inherited.
type budgetedUsageEmbedder struct {
	*budgetedEmbedder
	inner usageEmbedder
}

func (m *budgetedUsageEmbedder) EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) {
	lease, err := m.budget.Acquire(ctx, m.inner.Name())
	if err != nil {
		return nil, 0, err
	}
	outcome := errUnfinished
	defer func() { lease.Release(outcome) }()

	vecs, tokens, err := m.inner.EmbedUsage(ctx, texts)
	outcome = err
	// The tokens travel back even on the error path: a call that failed
	// halfway still cost what the endpoint says it cost.
	return vecs, tokens, err
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
