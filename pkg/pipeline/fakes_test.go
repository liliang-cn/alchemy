package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// openString is the Source.Open of a corpus that lives in the test file. The
// reader is built on each call because Run opens a source when it reads it,
// and a test that handed out one exhausted reader would prove nothing about
// the second read.
func openString(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

// openBytes is openString for a fixture that is not text.
func openBytes(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

// failLLM fails the test if it is called at all. It is how the routing test
// asserts DESIGN.md §2.1's first lesson: a structured source states its own
// entities and relations, and asking a model to infer what is written down is
// strictly worse. A counter checked afterwards would let the call happen and
// then complain; this refuses to let it happen quietly.
type failLLM struct{ t *testing.T }

func (f *failLLM) Name() string { return "must-not-be-called" }

func (f *failLLM) Complete(context.Context, alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	f.t.Helper()
	f.t.Error("the pipeline called an LLM for a job whose sources are all structured")
	return alchemy.LLMResponse{}, fmt.Errorf("must not be called")
}

// failEmbedder is the same refusal for the embedding stage.
type failEmbedder struct{ t *testing.T }

func (f *failEmbedder) Name() string { return "must-not-be-called" }

func (f *failEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	f.t.Helper()
	f.t.Error("the pipeline called an embedder for a job with nothing to embed")
	return nil, fmt.Errorf("must not be called")
}

// fakeEmbedder returns a vector per text and remembers what it was asked to
// embed, which is what the §5c test reads: the question is not how many
// vectors came back but which text they describe.
type fakeEmbedder struct {
	name string

	mu   sync.Mutex
	seen []string
}

func (f *fakeEmbedder) Name() string {
	if f.name == "" {
		return "fake-embedder"
	}
	return f.name
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.seen = append(f.seen, texts...)
	f.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len(t)), 1}
	}
	return out, nil
}

func (f *fakeEmbedder) embedded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// scriptLLM answers by matching text in the prompt rather than by call order,
// because the extractor runs chunks concurrently and a fake keyed on order
// would make every document test depend on a scheduler.
type scriptLLM struct {
	name string
	// replies maps a substring of the chunk to the raw reply the model gives
	// for the chunk containing it. A chunk matching nothing gets the honest
	// empty answer, which is what a real model does with a chunk that states
	// nothing the vocabulary can express.
	replies map[string]string
	// errs maps a substring to an endpoint failure.
	errs   map[string]error
	tokens int

	mu    sync.Mutex
	calls int
	// hook runs before every reply, for the tests about cancellation.
	hook func()
}

func (s *scriptLLM) Name() string {
	if s.name == "" {
		return "fake-llm"
	}
	return s.name
}

func (s *scriptLLM) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return alchemy.LLMResponse{}, err
	}
	s.mu.Lock()
	s.calls++
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	for _, match := range ordered(s.errs) {
		if strings.Contains(req.Prompt, match) {
			return alchemy.LLMResponse{}, s.errs[match]
		}
	}
	for _, match := range ordered(s.replies) {
		if strings.Contains(req.Prompt, match) {
			return alchemy.LLMResponse{Text: s.replies[match], Tokens: s.tokens}, nil
		}
	}
	return alchemy.LLMResponse{Text: `{"entities":[],"relations":[]}`, Tokens: s.tokens}, nil
}

// ordered puts the longest match first and breaks ties lexicographically, so
// that a chunk containing two of the fake's keys always gets the same answer.
// Ranging over the map would make the reply depend on Go's map order, and a
// test that fails one run in eight is a test people delete.
func ordered[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}

func (s *scriptLLM) called() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// asHeld is errors.As with the one target this package's tests ever use.
func asHeld(err error, held **HeldError) bool { return errors.As(err, held) }
