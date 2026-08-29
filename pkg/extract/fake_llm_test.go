package extract

import (
	"context"
	"fmt"
	"sync"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// fakeLLM answers with a canned reply per chunk. It keys on the chunk index
// carried in the prompt rather than on call order, because the whole point of
// the concurrency tests is that call order is not stable and the result must be
// anyway.
type fakeLLM struct {
	name string
	// replies maps chunk index to the raw text the model "returns".
	replies map[int]string
	// errs maps chunk index to an endpoint failure.
	errs map[int]error
	// tokens is reported per call; 0 means the provider reports no usage.
	tokens int

	mu      sync.Mutex
	prompts []alchemy.LLMRequest
}

func (f *fakeLLM) Name() string {
	if f.name == "" {
		return "fake-model"
	}
	return f.name
}

func (f *fakeLLM) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return alchemy.LLMResponse{}, err
	}
	f.mu.Lock()
	f.prompts = append(f.prompts, req)
	f.mu.Unlock()

	idx, err := chunkIndexOf(req.Prompt)
	if err != nil {
		return alchemy.LLMResponse{}, err
	}
	if e, ok := f.errs[idx]; ok {
		return alchemy.LLMResponse{}, e
	}
	reply, ok := f.replies[idx]
	if !ok {
		return alchemy.LLMResponse{}, fmt.Errorf("fake llm: no canned reply for chunk %d", idx)
	}
	return alchemy.LLMResponse{Text: reply, Tokens: f.tokens}, nil
}

// requests returns the calls made, for the prompt tests only. No test asserts
// on how many there were: a mock call count says nothing about whether the
// extraction was right.
func (f *fakeLLM) requests() []alchemy.LLMRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]alchemy.LLMRequest(nil), f.prompts...)
}

// chunkIndexOf recovers the chunk index the extractor put in the prompt. The
// fake needs some way to tell chunks apart, and the marker the real prompt
// already carries is better than an out-of-band counter.
func chunkIndexOf(prompt string) (int, error) {
	var idx int
	for i := 0; i+len(chunkMarker) <= len(prompt); i++ {
		if prompt[i:i+len(chunkMarker)] == chunkMarker {
			if _, err := fmt.Sscanf(prompt[i+len(chunkMarker):], "%d", &idx); err != nil {
				return 0, fmt.Errorf("fake llm: unreadable chunk marker: %w", err)
			}
			return idx, nil
		}
	}
	return 0, fmt.Errorf("fake llm: prompt carries no %q marker:\n%s", chunkMarker, prompt)
}

// testVocab is the prose vocabulary the extraction tests run under.
func testVocab() ontology.Vocabulary {
	return ontology.Vocabulary{
		Entities: []ontology.EntityType{
			{Name: "Cluster", Description: "a group of nodes", Attributes: []string{"region"}},
			{Name: "Node"},
			{Name: "Person"},
		},
		Relations: []ontology.RelationType{
			{Name: "DEPLOYED_ON", From: []string{"Cluster"}, To: []string{"Node"}},
			{Name: "MENTIONS"},
		},
	}
}

func testChunks(texts ...string) []alchemy.Chunk {
	out := make([]alchemy.Chunk, len(texts))
	at := 0
	for i, t := range texts {
		out[i] = alchemy.Chunk{
			Index:    i,
			Text:     t,
			Source:   "architecture.md",
			Strategy: "heading",
			Start:    at,
			End:      at + len(t),
		}
		at += len(t)
	}
	return out
}

func testOptions(llm alchemy.LLM) Options {
	return Options{LLM: llm, Vocabulary: testVocab(), OntologyID: "sds@3"}
}
