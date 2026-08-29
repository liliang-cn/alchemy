package model

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// temperature and max_tokens are per-endpoint knobs, and the endpoint is the
// caller's. What matters as much as setting them is not setting them: an
// endpoint given no temperature must receive no temperature field, because a
// gateway's own default is a better answer than a zero this package invented.
func TestOptionsSetChatParameters(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, oneChoice)

	l, err := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL, Options: map[string]string{
		"temperature": "0.2",
		"max_tokens":  "1024",
	}})
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.body["temperature"] != 0.2 {
		t.Errorf("temperature = %v, want 0.2", got.body["temperature"])
	}
	if got.body["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens = %v, want 1024", got.body["max_tokens"])
	}

	var plain capture
	srv2 := chatServer(t, &plain, oneChoice)
	l2, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv2.URL})
	if _, err := l2.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := plain.body["temperature"]; ok {
		t.Errorf("an unconfigured endpoint was sent a temperature: %s", plain.raw)
	}
	if _, ok := plain.body["max_tokens"]; ok {
		t.Errorf("an unconfigured endpoint was sent a max_tokens: %s", plain.raw)
	}
}

// dimensions is the embeddings knob, and it has to reach the wire: a store
// built for 768 columns and an endpoint defaulting to 1536 is a job that
// fails at the far end, long after the call was paid for.
func TestOptionsSetEmbeddingDimensions(t *testing.T) {
	var got capture
	srv := embedServer(t, &got, []map[string]any{datum(0, 1)}, 0)

	e, err := NewEmbedder(Endpoint{Name: "m", BaseURL: srv.URL, Options: map[string]string{"dimensions": "768"}})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got.body["dimensions"] != float64(768) {
		t.Errorf("dimensions = %v, want 768: %s", got.body["dimensions"], got.raw)
	}
}

// Not every gateway mounts the API where OpenAI does. A path override is what
// keeps "point alchemy at your own gateway" true for the ones that do not.
func TestOptionsOverrideThePath(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, oneChoice)

	l, err := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL, Options: map[string]string{"path": "/openai/deployments/x/chat"}})
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.path != "/openai/deployments/x/chat" {
		t.Errorf("posted to %q, want the overridden path", got.path)
	}
}

// Gateways demand their own headers — a tenant id, an Azure api-key, a
// routing hint. Without this the answer to "our gateway needs one header" is
// a fork of this package.
func TestOptionsAddExtraHeaders(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, oneChoice)

	l, err := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL, Options: map[string]string{
		"header.X-Tenant": "acme",
	}})
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.header.Get("X-Tenant") != "acme" {
		t.Errorf("X-Tenant = %q, want acme", got.header.Get("X-Tenant"))
	}
}

// An option nobody reads is §2.1's three-month fuse: the config says one
// thing, the endpoint does another, and the gap is only found when somebody
// wonders why the temperature never changed anything. A misspelling, a knob
// that belongs to a different model kind, and a value of the wrong type are
// all the same mistake and all fail where it was made.
func TestUnknownAndMisplacedOptionsAreRejectedAtConstruction(t *testing.T) {
	cases := []struct {
		name  string
		build func(Endpoint) (any, error)
		opts  map[string]string
		says  string
	}{
		{"typo", func(e Endpoint) (any, error) { return NewLLM(e) },
			map[string]string{"temperatur": "0.2"}, "temperatur"},
		{"embedding knob on a chat model", func(e Endpoint) (any, error) { return NewLLM(e) },
			map[string]string{"dimensions": "768"}, "dimensions"},
		{"chat knob on an embedder", func(e Endpoint) (any, error) { return NewEmbedder(e) },
			map[string]string{"temperature": "0.2"}, "temperature"},
		{"unparseable float", func(e Endpoint) (any, error) { return NewLLM(e) },
			map[string]string{"temperature": "warm"}, "temperature"},
		{"unparseable int", func(e Endpoint) (any, error) { return NewLLM(e) },
			map[string]string{"max_tokens": "lots"}, "max_tokens"},
		{"unparseable duration", func(e Endpoint) (any, error) { return NewOCR(e) },
			map[string]string{"timeout": "soon"}, "timeout"},
		{"a header with no name", func(e Endpoint) (any, error) { return NewOCR(e) },
			map[string]string{"header.": "x"}, "header"},
		{"relative path", func(e Endpoint) (any, error) { return NewLLM(e) },
			map[string]string{"path": "chat/completions"}, "path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(Endpoint{Name: "m", BaseURL: "https://gateway.example.com/v1", Options: tc.opts})
			if err == nil {
				t.Fatalf("construction accepted %v", tc.opts)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the error does not name the option at fault: %v", err)
			}
		})
	}
}

// The knobs that do belong to each kind must be accepted by that kind, or the
// rejection above has quietly become a ban.
func TestEachKindAcceptsItsOwnOptions(t *testing.T) {
	shared := map[string]string{"path": "/v1/x", "timeout": "30s", "header.X-A": "b"}
	if _, err := NewLLM(Endpoint{Name: "m", BaseURL: "https://h/v1", Options: withOpts(shared, "temperature", "0.1", "max_tokens", "8")}); err != nil {
		t.Errorf("NewLLM rejected its own options: %v", err)
	}
	if _, err := NewOCR(Endpoint{Name: "m", BaseURL: "https://h/v1", Options: withOpts(shared, "temperature", "0", "max_tokens", "4096")}); err != nil {
		t.Errorf("NewOCR rejected its own options: %v", err)
	}
	if _, err := NewEmbedder(Endpoint{Name: "m", BaseURL: "https://h/v1", Options: withOpts(shared, "dimensions", "768")}); err != nil {
		t.Errorf("NewEmbedder rejected its own options: %v", err)
	}
}

func withOpts(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// A header option that could overwrite Authorization turns a config typo into
// a call that sends the buyer's key to a header of somebody else's choosing,
// and one that could overwrite Content-Type posts JSON labelled as something
// it is not. Both are this client's own contract with the endpoint.
func TestHeaderOptionsCannotOverrideTheClientsOwnHeaders(t *testing.T) {
	for _, k := range []string{"header.Authorization", "header.authorization", "header.Content-Type"} {
		_, err := NewLLM(Endpoint{Name: "m", BaseURL: "https://h/v1", Options: map[string]string{k: "x"}})
		if err == nil {
			t.Errorf("NewLLM accepted %q", k)
			continue
		}
		if !IsConfigError(err) {
			t.Errorf("%q produced something other than a configuration error: %v", k, err)
		}
	}
}

// Content-Type is not optional decoration: an endpoint that receives a JSON
// body without it may well parse nothing and answer 400.
func TestRequestsAreLabelledAsJSON(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, oneChoice)
	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
