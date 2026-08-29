package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// capture is what a test server saw. Every test in this package asserts on the
// request as well as the reply: what we send is the half of the contract the
// endpoint cannot tell us we got wrong.
type capture struct {
	path   string
	auth   string
	header http.Header
	body   map[string]any
	raw    []byte
}

// chatServer answers /chat/completions with reply and records the request.
func chatServer(t *testing.T, got *capture, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.header = r.Header.Clone()
		got.raw = body
		_ = json.Unmarshal(body, &got.body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const oneChoice = `{"choices":[{"message":{"role":"assistant","content":"the answer"}}],
  "usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`

// The System/Prompt split is not cosmetic: pkg/extract puts the ontology
// vocabulary in System and the chunk in Prompt, and a provider that sees them
// merged into one user turn has lost the frame the 74%→94% story rests on.
func TestCompletePostsSystemAndUserMessages(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, oneChoice)

	l, err := NewLLM(Endpoint{Name: "gpt-4o-mini", BaseURL: srv.URL + "/v1", APIKey: "sk-secret"})
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	resp, err := l.Complete(context.Background(), alchemy.LLMRequest{
		System: "you extract a knowledge graph",
		Prompt: "Chunk index: 0\nnode-a runs SuperAI",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got.path != "/v1/chat/completions" {
		t.Errorf("posted to %q, want %q", got.path, "/v1/chat/completions")
	}
	if got.auth != "Bearer sk-secret" {
		t.Errorf("Authorization = %q", got.auth)
	}
	if got.body["model"] != "gpt-4o-mini" {
		t.Errorf("model = %v, want gpt-4o-mini", got.body["model"])
	}
	msgs, _ := got.body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("sent %d messages, want 2 (system then user): %s", len(msgs), got.raw)
	}
	first, _ := msgs[0].(map[string]any)
	second, _ := msgs[1].(map[string]any)
	if first["role"] != "system" || first["content"] != "you extract a knowledge graph" {
		t.Errorf("first message = %v, want the system instruction", first)
	}
	if second["role"] != "user" || second["content"] != "Chunk index: 0\nnode-a runs SuperAI" {
		t.Errorf("second message = %v, want the prompt", second)
	}
	if resp.Text != "the answer" {
		t.Errorf("Text = %q, want %q", resp.Text, "the answer")
	}
	if resp.Tokens != 15 {
		t.Errorf("Tokens = %d, want 15 (usage.total_tokens)", resp.Tokens)
	}
}

// An empty System must not become an empty system turn: some endpoints reject
// a message with no content, and a job that supplied no instruction did not
// ask for one to be invented.
func TestCompleteOmitsAnEmptySystemMessage(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, oneChoice)

	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "hello"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	msgs, _ := got.body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1: %s", len(msgs), got.raw)
	}
	if got.auth != "" {
		t.Errorf("sent an Authorization header for a keyless endpoint: %q", got.auth)
	}
}

// §7.2 reports what a job spent. alchemy.ModelCall documents 0 as "the
// provider did not report", so a missing usage block must stay 0 rather than
// become a guess — an invented token count is a wrong number about money.
func TestCompleteReportsZeroTokensWhenTheProviderIsSilent(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, `{"choices":[{"message":{"content":"hi"}}]}`)

	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	resp, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Tokens != 0 {
		t.Errorf("Tokens = %d, want 0 for a reply with no usage block", resp.Tokens)
	}
	if resp.Text != "hi" {
		t.Errorf("Text = %q", resp.Text)
	}
}

// JSON sets response_format. It is a request, not a guarantee — unwrapping a
// server that ignored it is pkg/extract's job — but the client must ask.
func TestCompleteAsksForJSONWhenRequested(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, oneChoice)

	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x", JSON: true}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	rf, ok := got.body["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("no response_format in the request: %s", got.raw)
	}
	if rf["type"] != "json_object" {
		t.Errorf("response_format.type = %v, want json_object", rf["type"])
	}
}

// The same client must cope with a server that ignored response_format and
// answered with a fence anyway: this package returns the text as it came, and
// does not try to be the parser that lives in pkg/extract.
func TestCompleteReturnsUnparsedTextWhenTheServerIgnoredJSON(t *testing.T) {
	var got capture
	fenced := "```json\n{\"entities\": []}\n```"
	body, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": fenced}}},
	})
	srv := chatServer(t, &got, string(body))

	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	resp, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x", JSON: true})
	if err != nil {
		t.Fatalf("Complete failed on a server that ignored response_format: %v", err)
	}
	if resp.Text != fenced {
		t.Errorf("Text = %q, want the fenced reply verbatim", resp.Text)
	}
}

// No choices is not an empty answer: an endpoint that returned nothing has
// failed, and calling that "" hands the extractor a chunk that produced no
// entities for a reason nobody recorded.
func TestCompleteRejectsAReplyWithNoChoices(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, `{"choices":[]}`)

	l, _ := NewLLM(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := l.Complete(context.Background(), alchemy.LLMRequest{Prompt: "x"}); err == nil {
		t.Fatal("Complete accepted a reply with no choices")
	}
}
