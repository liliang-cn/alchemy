package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/embed"
)

// These are the three ports of §6 and the two optional interfaces the rest of
// the repository reaches this package through. Asserting them here means a
// change that breaks one is a compile error in this package rather than a
// wiring failure in the service.
var (
	_ alchemy.LLM         = (*llm)(nil)
	_ alchemy.Embedder    = (*embedder)(nil)
	_ alchemy.OCR         = (*ocr)(nil)
	_ embed.UsageEmbedder = (*embedder)(nil)
	_ budget.RateLimiter  = (*APIError)(nil)
)

// A rate limit found on the extraction endpoint but not on the embedding one
// is a cluster that backs off for half its traffic. §8.2's budget is declared
// per endpoint and every endpoint has to be able to report the 429 that puts
// it into backoff, so all three kinds are checked, not just the chat one.
func TestEveryKindReportsARateLimitToTheBudget(t *testing.T) {
	srv := failingServer(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "7"}, "slow down")

	calls := map[string]func() error{
		"LLM": func() error {
			l, err := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
			if err != nil {
				return err
			}
			_, err = l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"})
			return err
		},
		"Embedder": func() error {
			e, err := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL})
			if err != nil {
				return err
			}
			_, err = e.Embed(context.Background(), []string{"a"})
			return err
		},
		"OCR": func() error {
			o, err := NewOCR(Endpoint{Name: "m", BaseURL: srv.URL})
			if err != nil {
				return err
			}
			_, err = o.Recognize(context.Background(), []byte{1}, "image/png")
			return err
		},
	}
	for kind, call := range calls {
		err := call()
		if err == nil {
			t.Fatalf("%s: the call succeeded against a 429", kind)
		}
		after, ok := budget.IsRateLimit(err)
		if !ok {
			t.Errorf("%s: budget.IsRateLimit did not recognise the 429: %v", kind, err)
		}
		if after.Seconds() != 7 {
			t.Errorf("%s: RetryAfter = %v, want 7s", kind, after)
		}
		if !Retryable(err) {
			t.Errorf("%s: a 429 is not reported as retryable", kind)
		}
	}
}

// A base URL is copied out of a provider's documentation, and half of them
// write the trailing slash. "https://host/v1/" + "/embeddings" is a 404 that
// looks like a wrong model name.
func TestBaseURLToleratesATrailingSlash(t *testing.T) {
	var got capture
	srv := embedServer(t, &got, []map[string]any{datum(0, 1)}, 0)

	e, err := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL + "/v1/"})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got.path != "/v1/embeddings" {
		t.Errorf("posted to %q, want %q", got.path, "/v1/embeddings")
	}
}

// A vector with no values is not a vector. Returning it would put an empty
// slice on a chunk that then looks embedded and matches nothing, which is the
// same silent wrongness as a mis-ordered batch wearing a different mask.
func TestEmbedRejectsAVectorWithNoValues(t *testing.T) {
	var got capture
	srv := embedServer(t, &got, []map[string]any{
		datum(0, 1, 1),
		{"index": 1, "embedding": nil},
	}, 0)

	e, _ := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL})
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("Embed accepted an empty vector")
	}
	// "index 1 was not answered" and "index 1 came back empty" send a reader
	// to different places: one is a provider dropping entries, the other a
	// dimensions setting the endpoint would not honour.
	if !strings.Contains(err.Error(), "no values") {
		t.Errorf("the error blames a missing entry rather than an empty vector: %v", err)
	}
	// Nothing about a malformed reply gets better by being asked again.
	if Retryable(err) {
		t.Errorf("a reply that cannot be aligned is reported as retryable: %v", err)
	}
}

// A construction error must be tellable from a call failure without reading
// the message: one means "your config is wrong", the other "the endpoint is".
func TestConfigurationErrorsAreOneClass(t *testing.T) {
	for _, e := range []Endpoint{
		{Name: "", BaseURL: "https://h/v1"},
		{Name: "m", BaseURL: ""},
		{Name: "m", BaseURL: "https://h/v1", Options: map[string]string{"nope": "1"}},
	} {
		_, err := NewLLM(e)
		if err == nil {
			t.Fatalf("NewLLM accepted %+v", e)
		}
		if !IsConfigError(err) {
			t.Errorf("IsConfigError is false for a construction failure: %v", err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	_, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"})
	if IsConfigError(err) {
		t.Errorf("IsConfigError is true for a 500: %v", err)
	}
}
