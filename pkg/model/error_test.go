package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
)

// failingServer answers every request with the status, headers and body given.
func failingServer(t *testing.T, status int, headers map[string]string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func completeAgainst(t *testing.T, srv *httptest.Server, e Endpoint) error {
	t.Helper()
	if e.Name == "" {
		e.Name = "gpt-4o-mini"
	}
	e.BaseURL = srv.URL + "/v1"
	l, err := NewLLM(e)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	_, err = l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("the call succeeded against a failing server")
	}
	return err
}

// An error that says only "request failed" sends the reader to a packet
// capture. The status says what happened, the endpoint says which of the
// three models a job supplied it happened to, and the body is the only place
// a provider ever explains itself.
func TestNonSuccessNamesTheStatusAndTheEndpoint(t *testing.T) {
	srv := failingServer(t, http.StatusBadRequest, nil, `{"error":{"message":"unknown model"}}`)
	err := completeAgainst(t, srv, Endpoint{Name: "gpt-4o-mini"})

	for _, want := range []string{"400", "gpt-4o-mini", srv.URL, "unknown model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// A provider that answers with an HTML error page must not put a megabyte into
// a log line. The body is evidence, not a payload.
func TestNonSuccessTruncatesTheBody(t *testing.T) {
	huge := "<html>" + strings.Repeat("A", 1<<20) + "</html>"
	srv := failingServer(t, http.StatusBadGateway, nil, huge)
	err := completeAgainst(t, srv, Endpoint{})

	if len(err.Error()) > 4096 {
		t.Errorf("the error is %d bytes; a provider's HTML error page reached the log intact", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("the error does not say the body was cut, so a reader cannot tell it is incomplete: %v", err)
	}
}

// §8.2's coordinated backoff is only as good as the detection in front of it:
// pkg/budget puts an endpoint into backoff for every worker when a call comes
// back rate limited, and a rate limit that stops being detected is the retry
// storm. This is the integration point, so it is asserted through the real
// budget.IsRateLimit rather than through this package's own types.
func TestRateLimitIsVisibleToTheBudget(t *testing.T) {
	srv := failingServer(t, http.StatusTooManyRequests, nil, `{"error":"slow down"}`)
	err := completeAgainst(t, srv, Endpoint{})

	if _, ok := budget.IsRateLimit(err); !ok {
		t.Fatalf("budget.IsRateLimit did not recognise a 429: %v", err)
	}
}

// Retry-After has two legal forms and providers use both. The endpoint's own
// number is better information than any schedule we could compute, so both
// have to arrive intact at RetryAfter().
func TestRetryAfterReachesTheBudgetInBothForms(t *testing.T) {
	t.Run("seconds", func(t *testing.T) {
		srv := failingServer(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "42"}, "slow down")
		err := completeAgainst(t, srv, Endpoint{})
		after, ok := budget.IsRateLimit(err)
		if !ok {
			t.Fatalf("not recognised as a rate limit: %v", err)
		}
		if after != 42*time.Second {
			t.Errorf("RetryAfter = %v, want 42s", after)
		}
	})

	t.Run("HTTP date", func(t *testing.T) {
		when := time.Now().UTC().Add(30 * time.Second).Format(http.TimeFormat)
		srv := failingServer(t, http.StatusTooManyRequests, map[string]string{"Retry-After": when}, "slow down")
		err := completeAgainst(t, srv, Endpoint{})
		after, ok := budget.IsRateLimit(err)
		if !ok {
			t.Fatalf("not recognised as a rate limit: %v", err)
		}
		// The header has one-second resolution and the round trip costs some
		// of it, so the assertion is a window rather than an equality.
		if after < 25*time.Second || after > 31*time.Second {
			t.Errorf("RetryAfter = %v, want about 30s from an HTTP-date header", after)
		}
	})

	t.Run("a date already past is not a negative wait", func(t *testing.T) {
		when := time.Now().UTC().Add(-time.Hour).Format(http.TimeFormat)
		srv := failingServer(t, http.StatusTooManyRequests, map[string]string{"Retry-After": when}, "slow down")
		err := completeAgainst(t, srv, Endpoint{})
		after, _ := budget.IsRateLimit(err)
		if after != 0 {
			t.Errorf("RetryAfter = %v for a date in the past, want 0 so the budget uses its own schedule", after)
		}
	})

	t.Run("no header", func(t *testing.T) {
		srv := failingServer(t, http.StatusTooManyRequests, nil, "slow down")
		err := completeAgainst(t, srv, Endpoint{})
		after, ok := budget.IsRateLimit(err)
		if !ok {
			t.Fatalf("not recognised as a rate limit: %v", err)
		}
		if after != 0 {
			t.Errorf("RetryAfter = %v with no header, want 0 — that is how the budget knows to use its own schedule", after)
		}
	})
}

// A 4xx is the caller's mistake and will fail identically forever; a 5xx or a
// 429 is the endpoint having a bad minute. Retrying the first wastes money on
// a call that cannot succeed, and not retrying the second throws away a job
// over a blip. The distinction is exposed rather than left to status-code
// arithmetic at every call site — the decision itself still belongs to
// pkg/budget (§8.2); this only reports what kind of failure it was.
func TestRetryabilityIsVisibleToACaller(t *testing.T) {
	cases := []struct {
		status int
		retry  bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tc := range cases {
		srv := failingServer(t, tc.status, nil, "nope")
		err := completeAgainst(t, srv, Endpoint{})

		if got := Retryable(err); got != tc.retry {
			t.Errorf("Retryable(%d) = %v, want %v", tc.status, got, tc.retry)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("a %d did not produce an *APIError: %v", tc.status, err)
		}
		if apiErr.Status != tc.status {
			t.Errorf("APIError.Status = %d, want %d", apiErr.Status, tc.status)
		}
		if apiErr.Model != "gpt-4o-mini" {
			t.Errorf("APIError.Model = %q", apiErr.Model)
		}
	}
	// A transport failure — no server at all — is not the caller's mistake
	// either, and must not be classified as permanent.
	srv := failingServer(t, 200, nil, "")
	srv.Close()
	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	_, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("a call to a closed server succeeded")
	}
	if !Retryable(err) {
		t.Errorf("a transport failure is not retryable: %v", err)
	}
}

// An error travels: into a log, into a job result, to the buyer's support
// desk. A key that rides along in it has leaked, and a provider that echoes
// the Authorization header back in its own error body is not a hypothetical.
func TestTheAPIKeyNeverAppearsInAnError(t *testing.T) {
	const key = "sk-live-do-not-log-me"
	// The server echoes the key back exactly the way a chatty gateway does.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key ` + r.Header.Get("Authorization") + ` for this org"}`))
	}))
	t.Cleanup(srv.Close)

	err := completeAgainst(t, srv, Endpoint{APIKey: key})
	if strings.Contains(err.Error(), key) {
		t.Fatalf("the API key is in the error message: %v", err)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("not an *APIError: %v", err)
	}
	if strings.Contains(apiErr.Body, key) {
		t.Fatalf("the API key is in APIError.Body: %s", apiErr.Body)
	}
}
