package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// A cancelled job must stop paying for calls immediately, and the expensive
// half of a model call is the reply, not the handshake. A client that only
// passes ctx to the dial keeps reading a body for a job that was cancelled
// minutes ago — and §7.2 promised the caller can cancel a job whose bill is
// growing faster than expected, which is a promise about the calls in flight.
func TestCallIsCancelledWhileReadingTheBody(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Headers and a fragment of the body arrive; the rest never does.
		_, _ = io.WriteString(w, `{"choices":[`)
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	done := make(chan error, 1)
	go func() {
		_, err := l.Complete(ctx, alchemy.LLMRequest{Prompt: "x"})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Complete returned %v, want a context.Canceled", err)
		}
		if Retryable(err) {
			t.Error("a call the caller cancelled is reported as retryable; the job is going away")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Complete is still reading the body of a cancelled call")
	}
}

// A context already done before the call must not reach the endpoint at all.
func TestCallAlreadyCancelledNeverReachesTheEndpoint(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// All three models, because ctx handling that was only wired into one of
	// them is the kind of gap nobody notices until an OCR job will not stop.
	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := l.Complete(ctx, alchemy.LLMRequest{Prompt: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Complete returned %v, want context.Canceled", err)
	}
	e, _ := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := e.Embed(ctx, []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Embed returned %v, want context.Canceled", err)
	}
	o, _ := NewOCR(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := o.Recognize(ctx, []byte{1}, "image/png"); !errors.Is(err, context.Canceled) {
		t.Errorf("Recognize returned %v, want context.Canceled", err)
	}
	if called {
		t.Error("a call with an already-cancelled context reached the endpoint")
	}
}

// The client must not wait forever on an endpoint that accepted the connection
// and then said nothing: a hung call holds a budget slot (§8.2), so one silent
// endpoint drains the cluster's concurrency for every job on it.
func TestATimeoutIsAppliedAndIsRetryable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	l, err := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL, Options: map[string]string{"timeout": "150ms"}})
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	start := time.Now()
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"}); err == nil {
		t.Fatal("Complete waited for a silent endpoint and then succeeded")
	} else if !Retryable(err) {
		t.Errorf("a timeout is not reported as retryable: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the call took %v; the timeout option was not applied", elapsed)
	}
}
