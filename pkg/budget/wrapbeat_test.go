package budget_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
)

// The failure a naive TTL introduces is worse than the one it fixes: a
// ninety-second OCR call would start failing on a budget that used to work. The
// tests below are that scenario — a call that outlives its TTL several times
// over and keeps its slot — and its converse, a slot genuinely taken away.

// liveBudget hands out leases that expire, so the decorators can be tested
// against the shape a shared store has without needing one.
type liveBudget struct{ lease *liveLease }

func (b *liveBudget) Acquire(context.Context, string) (budget.Lease, error) { return b.lease, nil }

// slowLLM holds the call open past the lease's TTL and records the context it
// was given, which is how the test can tell whether the wrapper handed the
// model the keepalive's context or the caller's.
type slowLLM struct {
	name string
	hold time.Duration
	seen chan context.Context
}

func (m *slowLLM) Name() string { return m.name }

func (m *slowLLM) Complete(ctx context.Context, _ alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	m.seen <- ctx
	select {
	case <-time.After(m.hold):
		return alchemy.LLMResponse{Tokens: 3}, nil
	case <-ctx.Done():
		return alchemy.LLMResponse{}, context.Cause(ctx)
	}
}

func TestTheDecoratorsHeartbeatForAsLongAsTheCallRuns(t *testing.T) {
	const ttl = 30 * time.Millisecond
	l := &liveLease{ttl: ttl}
	inner := &slowLLM{name: "gpt", hold: 6 * ttl, seen: make(chan context.Context, 1)}
	wrapped := budget.WrapLLM(inner, &liveBudget{lease: l})

	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Complete(context.Background(), alchemy.LLMRequest{})
		done <- err
	}()

	<-inner.seen
	awaitBeats(t, l, 3)
	if err := <-done; err != nil {
		t.Fatalf("a call that outlived its TTL failed: %v", err)
	}
	if l.released.Load() != 1 {
		t.Fatalf("the slot was released %d times, want exactly 1", l.released.Load())
	}
	// No renewal may arrive after the slot went back: it would extend a lease
	// nobody holds and shrink the cluster's budget by one for a whole TTL.
	settled := l.beats.Load()
	time.Sleep(4 * ttl)
	if got := l.beats.Load(); got != settled {
		t.Fatalf("%d heartbeats arrived after Release", got-settled)
	}
}

func TestAModelCallIsCutShortWhenTheSlotIsTakenAway(t *testing.T) {
	l := &liveLease{ttl: 20 * time.Millisecond}
	l.fail.Store(budget.ErrLeaseExpired)
	inner := &slowLLM{name: "gpt", hold: time.Minute, seen: make(chan context.Context, 1)}
	wrapped := budget.WrapLLM(inner, &liveBudget{lease: l})

	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Complete(context.Background(), alchemy.LLMRequest{})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, budget.ErrLeaseExpired) {
			t.Fatalf("Complete = %v, want ErrLeaseExpired: a node that lost its slot must stop calling the endpoint", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the slot was reclaimed and the call carried on regardless")
	}
}

func TestTheEmbedderAndOCRHeartbeatToo(t *testing.T) {
	const ttl = 30 * time.Millisecond

	embLease := &liveLease{ttl: ttl}
	emb := budget.WrapEmbedder(&fakeEmbedder{name: "embed", call: func() ([][]float32, error) {
		time.Sleep(3 * ttl)
		return nil, nil
	}}, &liveBudget{lease: embLease})
	if _, err := emb.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := embLease.beats.Load(); got < 2 {
		t.Fatalf("the embedder renewed its slot %d times over three TTLs: it would have lost it", got)
	}

	ocrLease := &liveLease{ttl: ttl}
	ocr := budget.WrapOCR(&fakeOCR{name: "ocr", call: func() (string, error) {
		time.Sleep(3 * ttl)
		return "text", nil
	}}, &liveBudget{lease: ocrLease})
	if _, err := ocr.Recognize(context.Background(), nil, "image/png"); err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if got := ocrLease.beats.Load(); got < 2 {
		t.Fatalf("the OCR model renewed its slot %d times over three TTLs: it would have lost it", got)
	}
}
