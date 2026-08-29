package budget_test

import (
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// providerError is what an adapter for someone else's SDK looks like. It is
// written here without embedding or referencing anything from pkg/budget, which
// is the whole argument for detecting rate limits through an interface: an
// adapter opts in structurally, with no import and no dependency.
type providerError struct {
	status int
	after  time.Duration
}

func (e providerError) Error() string             { return fmt.Sprintf("provider returned %d", e.status) }
func (e providerError) RateLimited() bool         { return e.status == 429 }
func (e providerError) RetryAfter() time.Duration { return e.after }

func TestRateLimitIsRecognisedThroughTheInterface(t *testing.T) {
	after, ok := budget.IsRateLimit(providerError{status: 429, after: 3 * time.Second})
	if !ok {
		t.Fatal("an error whose RateLimited() reports true was not recognised")
	}
	if after != 3*time.Second {
		t.Fatalf("RetryAfter = %v, want 3s", after)
	}

	// Wrapping must not hide it: errors travel up a stack inside %w.
	wrapped := fmt.Errorf("chunk 12: %w", providerError{status: 429})
	if _, ok := budget.IsRateLimit(wrapped); !ok {
		t.Fatal("a wrapped rate limit was not recognised")
	}

	// An error from the same type that is not a 429 must not be treated as one.
	if _, ok := budget.IsRateLimit(providerError{status: 500}); ok {
		t.Fatal("a 500 was treated as a rate limit")
	}
}

func TestRateLimitIsRecognisedThroughTheSentinel(t *testing.T) {
	err := fmt.Errorf("openai: %w", budget.ErrRateLimited)
	after, ok := budget.IsRateLimit(err)
	if !ok {
		t.Fatal("an error wrapping ErrRateLimited was not recognised")
	}
	if after != 0 {
		t.Fatalf("RetryAfter = %v, want 0 when the endpoint said nothing", after)
	}
}

func TestTooFastCarriesTheCauseAndTheEndpointsOwnNumber(t *testing.T) {
	err := budget.TooFast(io.ErrUnexpectedEOF, 12*time.Second)

	after, ok := budget.IsRateLimit(err)
	if !ok {
		t.Fatal("TooFast did not produce a recognised rate limit")
	}
	if after != 12*time.Second {
		t.Fatalf("RetryAfter = %v, want 12s", after)
	}
	if !errors.Is(err, budget.ErrRateLimited) {
		t.Fatal("TooFast is not errors.Is(ErrRateLimited)")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("TooFast lost the cause it was given")
	}
	if err.Error() == "" {
		t.Fatal("TooFast produced an empty message")
	}
}

func TestOrdinaryErrorsAreNotRateLimits(t *testing.T) {
	for _, err := range []error{nil, io.EOF, errors.New("429 too many requests"), fmt.Errorf("wrapped: %w", io.EOF)} {
		if _, ok := budget.IsRateLimit(err); ok {
			t.Fatalf("IsRateLimit(%v) = true; detection must not guess from message text", err)
		}
	}
}
