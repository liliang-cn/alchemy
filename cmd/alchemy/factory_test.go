package main

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/runner"
)

// The binary is where the choice of provider lives, and this is the whole of
// that choice: pkg/runner names an interface, pkg/model implements the calls,
// and this is the field-for-field translation between them.
func TestFactorySatisfiesTheRunner(t *testing.T) {
	var f runner.Factory = modelFactory{}

	llm, err := f.LLM(runner.Endpoint{Name: "gpt-4o-mini", BaseURL: "https://llm.example/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("LLM: %v", err)
	}
	if llm.Name() != "gpt-4o-mini" {
		t.Fatalf("name = %q, want the endpoint's", llm.Name())
	}
	if _, err := f.Embedder(runner.Endpoint{Name: "embed", BaseURL: "https://emb.example/v1"}); err != nil {
		t.Fatalf("Embedder: %v", err)
	}
	if _, err := f.OCR(runner.Endpoint{Name: "ocr", BaseURL: "https://ocr.example/v1"}); err != nil {
		t.Fatalf("OCR: %v", err)
	}
}

// A misconfigured endpoint has to come back as an error, so that the runner
// can fail the job at the start rather than an hour into a corpus.
func TestFactoryReportsAMisconfiguredEndpoint(t *testing.T) {
	if _, err := (modelFactory{}).LLM(runner.Endpoint{}); err == nil {
		t.Fatal("an endpoint with no name and no URL was accepted")
	}
}
