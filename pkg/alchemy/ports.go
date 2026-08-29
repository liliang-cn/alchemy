package alchemy

import "context"

// The three models a job may supply. §6: models are supplied per job, not
// configured globally — a buyer's LLM, embedding and OCR endpoints are their
// business, and a service that hardcodes them only works in the environment it
// was built in.
//
// These interfaces are deliberately the smallest thing the pipeline needs. They
// will grow when a stage needs something they cannot express, and not before.

// LLMRequest is one call to a language model.
type LLMRequest struct {
	// System is the instruction that frames the task; for extraction it carries
	// the ontology vocabulary (§2.1).
	System string
	// Prompt is the material to work on — usually one chunk.
	Prompt string
	// JSON asks the model to reply with JSON only. A provider that cannot
	// enforce it should still set it in the prompt.
	JSON bool
}

// LLMResponse is what came back, plus what it cost.
type LLMResponse struct {
	Text string
	// Tokens is 0 when the provider does not report usage.
	Tokens int
}

// LLM is a language model the caller supplied.
type LLM interface {
	// Name identifies the model in provenance and in the cost report.
	Name() string
	Complete(ctx context.Context, req LLMRequest) (LLMResponse, error)
}

// Embedder turns text into vectors. Batching is the implementation's business;
// the pipeline hands it everything it wants embedded at once.
type Embedder interface {
	Name() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// OCR reads text off a page image. When a job supplies none, a page with no
// text layer is reported in Result.Unread rather than returned as empty (§5).
type OCR interface {
	Name() string
	// Recognize is given the raw bytes of one page image and its media type.
	Recognize(ctx context.Context, page []byte, mediaType string) (string, error)
}

// Models is what a job was given. Any of them may be nil; a stage that needs a
// nil model fails loudly rather than degrading.
type Models struct {
	LLM      LLM
	Embedder Embedder
	OCR      OCR
}
