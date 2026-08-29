package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
)

func TestModelsAreBuiltFromTheJobsEndpoints(t *testing.T) {
	f := &recordingFactory{}
	got, err := buildModels(f, service.Models{
		LLM:      service.Model{Name: "gpt", Endpoint: "https://llm.example", APIKey: "k", Options: map[string]string{"temperature": "0"}},
		Embedder: service.Model{Name: "embed", Endpoint: "https://emb.example"},
	})
	if err != nil {
		t.Fatalf("buildModels: %v", err)
	}
	if got.LLM == nil || got.LLM.Name() != "gpt" {
		t.Fatalf("LLM = %v, want the factory's gpt", got.LLM)
	}
	if got.Embedder == nil {
		t.Fatal("Embedder was not built")
	}
	// §5: "an absent OCR is a configuration, not an error" — a scanned page is
	// then reported unread rather than returned as empty text. A wrapper around
	// nothing would satisfy the interface and move that failure to the first
	// scanned page.
	if got.OCR != nil {
		t.Fatalf("OCR = %v, want nil for an endpoint nobody supplied", got.OCR)
	}
	if f.llm.BaseURL != "https://llm.example" || f.llm.APIKey != "k" || f.llm.Options["temperature"] != "0" {
		t.Fatalf("LLM endpoint = %+v, want every field carried", f.llm)
	}
}

// A factory that cannot reach an endpoint is a job that cannot run, and saying
// so before the corpus is read is the whole reason models are built up front.
func TestModelsReportAFactoryFailure(t *testing.T) {
	f := &recordingFactory{err: errors.New("no such provider")}
	_, err := buildModels(f, service.Models{LLM: service.Model{Name: "gpt"}})
	if err == nil {
		t.Fatal("buildModels hid a factory failure")
	}
	if !errors.Is(err, f.err) {
		t.Fatalf("error = %v, want it to wrap the factory's", err)
	}
}

type recordingFactory struct {
	llm, embedder, ocr Endpoint
	err                error
}

func (f *recordingFactory) LLM(e Endpoint) (alchemy.LLM, error) {
	f.llm = e
	if f.err != nil {
		return nil, f.err
	}
	return stubLLM{e.Name}, nil
}

func (f *recordingFactory) Embedder(e Endpoint) (alchemy.Embedder, error) {
	f.embedder = e
	if f.err != nil {
		return nil, f.err
	}
	return stubEmbedder{e.Name}, nil
}

func (f *recordingFactory) OCR(e Endpoint) (alchemy.OCR, error) {
	f.ocr = e
	if f.err != nil {
		return nil, f.err
	}
	return stubOCR{e.Name}, nil
}

type stubLLM struct{ name string }

func (s stubLLM) Name() string { return s.name }
func (s stubLLM) Complete(context.Context, alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	return alchemy.LLMResponse{Text: "{}"}, nil
}

type stubEmbedder struct{ name string }

func (s stubEmbedder) Name() string { return s.name }
func (s stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

type stubOCR struct{ name string }

func (s stubOCR) Name() string { return s.name }
func (s stubOCR) Recognize(context.Context, []byte, string) (string, error) {
	return "", nil
}
