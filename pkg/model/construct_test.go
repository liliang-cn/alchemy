package model

import (
	"strings"
	"testing"
)

// A misconfigured endpoint has to fail where the mistake was made. An empty
// BaseURL that only surfaces as a request to "/chat/completions" against no
// host is the same bug reported from three stack frames away.
func TestConstructionRejectsEmptyBaseURL(t *testing.T) {
	_, err := NewLLM(Endpoint{Name: "gpt-4o-mini"})
	if err == nil {
		t.Fatal("NewLLM accepted an endpoint with no BaseURL")
	}
	if !strings.Contains(err.Error(), "base URL") {
		t.Errorf("error does not name the missing field: %v", err)
	}
}

// Name is not decoration: it is what lands in provenance (§7.2) and it is the
// key the budget leases on (§8.2). An unnamed model makes both meaningless.
func TestConstructionRejectsEmptyName(t *testing.T) {
	_, err := NewLLM(Endpoint{BaseURL: "https://gateway.example.com/v1"})
	if err == nil {
		t.Fatal("NewLLM accepted an endpoint with no Name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error does not name the missing field: %v", err)
	}
}

// A local Ollama has no API key, and requiring one would make the common
// self-hosted case unreachable.
func TestConstructionDoesNotRequireAnAPIKey(t *testing.T) {
	llm, err := NewLLM(Endpoint{Name: "qwen3", BaseURL: "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatalf("NewLLM refused a keyless endpoint: %v", err)
	}
	if llm.Name() != "qwen3" {
		t.Errorf("Name() = %q, want %q", llm.Name(), "qwen3")
	}
}
