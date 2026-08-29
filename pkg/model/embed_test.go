package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/embed"
)

// embedServer answers /embeddings with the data entries given, in the order
// given — which is the point: the tests hand it a shuffled list.
func embedServer(t *testing.T, got *capture, data []map[string]any, tokens int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.path = r.URL.Path
		got.raw = body
		got.header = r.Header.Clone()
		_ = json.Unmarshal(body, &got.body)
		reply := map[string]any{"data": data}
		if tokens > 0 {
			reply["usage"] = map[string]any{"total_tokens": tokens}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func datum(index int, values ...float64) map[string]any {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return map[string]any{"object": "embedding", "index": index, "embedding": vs}
}

// The API documents an index field precisely because the order of data[] is
// not guaranteed. A silently mis-ordered batch attaches every vector to the
// wrong chunk, and that is not an error anybody sees — it is a search result
// that is quietly wrong forever. So the server here answers 3,0,2,1 on purpose.
func TestEmbedReordersByIndexRatherThanTrustingArrivalOrder(t *testing.T) {
	var got capture
	srv := embedServer(t, &got, []map[string]any{
		datum(3, 3, 3), datum(0, 0, 0), datum(2, 2, 2), datum(1, 1, 1),
	}, 0)

	e, err := NewEmbedder(Endpoint{Name: "text-embedding-3-small", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	texts := []string{"zero", "one", "two", "three"}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if got.path != "/v1/embeddings" {
		t.Errorf("posted to %q, want %q", got.path, "/v1/embeddings")
	}
	// The whole batch goes in one call: the pipeline hands the embedder
	// everything it wants embedded at once.
	input, _ := got.body["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("sent %d inputs, want the whole batch of 4: %s", len(input), got.raw)
	}
	if len(vecs) != 4 {
		t.Fatalf("got %d vectors for 4 texts", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 2 || v[0] != float32(i) || v[1] != float32(i) {
			t.Errorf("vector %d = %v, want the one the server labelled index %d", i, v, i)
		}
	}
}

// A batch whose reply is missing an index, or names one out of range, cannot
// be aligned. Guessing here would produce exactly the silent mis-attachment
// the index field exists to prevent, so it is an error instead.
func TestEmbedRejectsAReplyItCannotAlign(t *testing.T) {
	cases := []struct {
		name string
		data []map[string]any
	}{
		{"short", []map[string]any{datum(0, 1), datum(1, 2)}},
		{"index out of range", []map[string]any{datum(0, 1), datum(1, 2), datum(7, 3)}},
		{"duplicate index", []map[string]any{datum(0, 1), datum(0, 2), datum(2, 3)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got capture
			srv := embedServer(t, &got, tc.data, 0)
			e, _ := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL})
			if _, err := e.Embed(context.Background(), []string{"a", "b", "c"}); err == nil {
				t.Fatal("Embed accepted a reply it could not align to its inputs")
			}
		})
	}
}

// §7.2: a job reports what it spent. pkg/embed asks for that through the
// optional UsageEmbedder, so this client has to satisfy it or every embedding
// call is reported as costing nothing.
func TestEmbedderSatisfiesUsageEmbedder(t *testing.T) {
	var got capture
	srv := embedServer(t, &got, []map[string]any{datum(0, 1, 1), datum(1, 2, 2)}, 42)

	e, _ := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL})
	u, ok := e.(embed.UsageEmbedder)
	if !ok {
		t.Fatal("the embedder does not implement embed.UsageEmbedder, so its tokens are never counted")
	}
	vecs, tokens, err := u.EmbedUsage(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedUsage: %v", err)
	}
	if tokens != 42 {
		t.Errorf("tokens = %d, want 42 (usage.total_tokens)", tokens)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
}

// An empty batch is not a call. Posting input:[] wastes a request and some
// endpoints reject it outright.
func TestEmbedMakesNoCallForAnEmptyBatch(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	e, _ := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL})
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if called {
		t.Error("Embed called the endpoint for an empty batch")
	}
	if len(vecs) != 0 {
		t.Errorf("got %d vectors for an empty batch", len(vecs))
	}
}
