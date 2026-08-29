package budget_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
)

// fakeLLM is the caller's model. call is whatever the test needs that call to
// do; every invocation is counted, because a wrapper that quietly retried would
// make the job's own count of model calls (§7.2) a lie.
type fakeLLM struct {
	name  string
	calls atomic.Int64
	call  func() (alchemy.LLMResponse, error)
}

func (m *fakeLLM) Name() string { return m.name }

func (m *fakeLLM) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	m.calls.Add(1)
	if m.call == nil {
		return alchemy.LLMResponse{Text: req.Prompt, Tokens: 7}, nil
	}
	return m.call()
}

type fakeEmbedder struct {
	name  string
	calls atomic.Int64
	call  func() ([][]float32, error)
}

func (m *fakeEmbedder) Name() string { return m.name }

func (m *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.calls.Add(1)
	if m.call == nil {
		return make([][]float32, len(texts)), nil
	}
	return m.call()
}

type fakeOCR struct {
	name  string
	calls atomic.Int64
	call  func() (string, error)
}

func (m *fakeOCR) Name() string { return m.name }

func (m *fakeOCR) Recognize(ctx context.Context, page []byte, mediaType string) (string, error) {
	m.calls.Add(1)
	if m.call == nil {
		return mediaType, nil
	}
	return m.call()
}

func TestWrappersPassTheModelNameThroughUnchanged(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})

	// Provenance and alchemy.ModelCall are keyed on this string. A wrapper that
	// renamed the model would corrupt every citation the job produces.
	if got := budget.WrapLLM(&fakeLLM{name: "gemini-3.6-flash-high"}, b).Name(); got != "gemini-3.6-flash-high" {
		t.Errorf("LLM Name = %q", got)
	}
	if got := budget.WrapEmbedder(&fakeEmbedder{name: "text-embedding-4"}, b).Name(); got != "text-embedding-4" {
		t.Errorf("Embedder Name = %q", got)
	}
	if got := budget.WrapOCR(&fakeOCR{name: "tesseract-6"}, b).Name(); got != "tesseract-6" {
		t.Errorf("OCR Name = %q", got)
	}
}

func TestTheWrapperHidesNoCost(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 2})
	inner := &fakeLLM{name: "gpt", call: func() (alchemy.LLMResponse, error) {
		return alchemy.LLMResponse{Text: "hello", Tokens: 1234}, nil
	}}
	wrapped := budget.WrapLLM(inner, b)

	resp, err := wrapped.Complete(context.Background(), alchemy.LLMRequest{Prompt: "p", JSON: true})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// §7.2: the caller must still be able to see what the job spent.
	if resp.Tokens != 1234 || resp.Text != "hello" {
		t.Fatalf("response = %+v, want the inner response unchanged", resp)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("inner called %d times for one Complete: the call count the job reports would be wrong", got)
	}

	// An error is passed back rather than retried behind the caller's back, for
	// the same reason: one Complete is one call on the bill.
	boom := errors.New("upstream exploded")
	inner.call = func() (alchemy.LLMResponse, error) { return alchemy.LLMResponse{Tokens: 9}, boom }
	resp, err = wrapped.Complete(context.Background(), alchemy.LLMRequest{})
	if !errors.Is(err, boom) {
		t.Fatalf("Complete err = %v, want the inner error", err)
	}
	if resp.Tokens != 9 {
		t.Fatalf("tokens = %d on a failed call, want the inner 9", resp.Tokens)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("inner called %d times in total, want 2", got)
	}
}

func TestTheWrappedModelObeysTheBudget(t *testing.T) {
	const (
		limit   = 3
		workers = 32
		rounds  = 20
	)
	b := newBudget(t, budget.Config{Limit: limit})
	var p peak
	inner := &fakeLLM{name: "gpt", call: func() (alchemy.LLMResponse, error) {
		p.enter()
		runtime.Gosched()
		p.leave()
		return alchemy.LLMResponse{Tokens: 1}, nil
	}}
	wrapped := budget.WrapLLM(inner, b)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				if _, err := wrapped.Complete(context.Background(), alchemy.LLMRequest{}); err != nil {
					t.Errorf("Complete: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := p.max.Load(); got > limit {
		t.Fatalf("peak in flight through the wrapper was %d, over the budget of %d", got, limit)
	} else if got < limit {
		t.Fatalf("peak in flight was only %d: the test never exercised the bound", got)
	}
	if got := inner.calls.Load(); got != workers*rounds {
		t.Fatalf("inner called %d times, want %d", got, workers*rounds)
	}
}

func TestAPanicInTheModelDoesNotLeakASlot(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	inner := &fakeLLM{name: "gpt", call: func() (alchemy.LLMResponse, error) {
		panic("the provider SDK panicked")
	}}
	wrapped := budget.WrapLLM(inner, b)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the wrapper swallowed the panic; a caller must still see it")
			}
		}()
		_, _ = wrapped.Complete(context.Background(), alchemy.LLMRequest{})
	}()

	if got := b.InFlight("gpt"); got != 0 {
		t.Fatalf("in flight = %d after a panic, want 0", got)
	}
	// The slot is not merely uncounted; it is usable.
	inner.call = nil
	for i := 0; i < 3; i++ {
		if _, err := wrapped.Complete(context.Background(), alchemy.LLMRequest{}); err != nil {
			t.Fatalf("Complete after a panic: %v", err)
		}
	}
}

func TestARateLimitFromTheModelClosesTheEndpointForEveryone(t *testing.T) {
	clk := newFakeClock()
	b := newBudget(t, budget.Config{
		Limit:   4,
		Backoff: budget.Backoff{Base: 6 * time.Second, Max: time.Minute},
		Clock:   clk,
		Rand:    fixedRand(1),
	})
	inner := &fakeLLM{name: "gpt", call: func() (alchemy.LLMResponse, error) {
		return alchemy.LLMResponse{}, budget.TooFast(errors.New("HTTP 429"), 0)
	}}
	wrapped := budget.WrapLLM(inner, b)

	// No stage had to know about backoff for this to happen: one call came back
	// 429 and the endpoint is now closed to every other worker.
	if _, err := wrapped.Complete(context.Background(), alchemy.LLMRequest{}); err == nil {
		t.Fatal("want the rate-limit error back")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = wrapped.Complete(context.Background(), alchemy.LLMRequest{})
	}()
	if got := clk.awaitTimers(t, 1); got[0] != 6*time.Second {
		t.Fatalf("the next caller is waiting %v, want 6s", got[0])
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("the model was called %d times; the second call should still be held back", got)
	}
	clk.Advance(6 * time.Second)
	<-done
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("the model was called %d times after the backoff expired, want 2", got)
	}
}

func TestAWrappedModelIsNotCalledWhenNoSlotCanBeHad(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	inner := &fakeLLM{name: "gpt"}
	wrapped := budget.WrapLLM(inner, b)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wrapped.Complete(ctx, alchemy.LLMRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete = %v, want context.Canceled", err)
	}
	if got := inner.calls.Load(); got != 0 {
		t.Fatalf("the model was called %d times without a slot", got)
	}
}

func TestEmbedderAndOCRAreBudgetedOnTheirOwnEndpoints(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	ctx := context.Background()

	emb := budget.WrapEmbedder(&fakeEmbedder{name: "embed"}, b)
	ocr := budget.WrapOCR(&fakeOCR{name: "ocr"}, b)

	// The extraction model is saturated; neither of the others is keyed on it.
	held, err := b.Acquire(ctx, "gpt")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held.Release(nil)

	vecs, err := emb.Embed(ctx, []string{"a", "b"})
	if err != nil || len(vecs) != 2 {
		t.Fatalf("Embed = %v, %v", vecs, err)
	}
	text, err := ocr.Recognize(ctx, []byte("page"), "image/png")
	if err != nil || text != "image/png" {
		t.Fatalf("Recognize = %q, %v", text, err)
	}
	if got := b.InFlight("embed"); got != 0 {
		t.Fatalf("embed in flight = %d after the call returned, want 0", got)
	}
	if got := b.InFlight("ocr"); got != 0 {
		t.Fatalf("ocr in flight = %d after the call returned, want 0", got)
	}

	// And each is genuinely bounded on its own key.
	l, err := b.Acquire(ctx, "embed")
	if err != nil {
		t.Fatalf("Acquire embed: %v", err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := emb.Embed(ctx, []string{"c"})
		blocked <- err
	}()
	awaitWaiters(t, b, "embed", 1)
	select {
	case err := <-blocked:
		t.Fatalf("Embed ran with no slot (err=%v)", err)
	default:
	}
	l.Release(nil)
	if err := <-blocked; err != nil {
		t.Fatalf("Embed after the slot freed: %v", err)
	}
}

func TestAPanicInTheEmbedderAndOCRDoesNotLeakASlot(t *testing.T) {
	b := newBudget(t, budget.Config{Limit: 1})
	emb := budget.WrapEmbedder(&fakeEmbedder{name: "embed", call: func() ([][]float32, error) {
		panic("embedder panicked")
	}}, b)
	ocr := budget.WrapOCR(&fakeOCR{name: "ocr", call: func() (string, error) {
		panic("ocr panicked")
	}}, b)

	for _, tc := range []struct {
		model string
		run   func()
	}{
		{"embed", func() { _, _ = emb.Embed(context.Background(), nil) }},
		{"ocr", func() { _, _ = ocr.Recognize(context.Background(), nil, "image/png") }},
	} {
		func() {
			defer func() { _ = recover() }()
			tc.run()
		}()
		if got := b.InFlight(tc.model); got != 0 {
			t.Fatalf("%s in flight = %d after a panic, want 0", tc.model, got)
		}
	}
}
