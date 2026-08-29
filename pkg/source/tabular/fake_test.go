package tabular

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// fakeLLM returns a canned mapping. Tests assert on the entities that came out
// of it, not on how many times it was asked: what a reviewer of this package
// cares about is the graph and the guesses, and a test that counts calls passes
// while the graph is wrong.
type fakeLLM struct {
	name   string
	reply  string
	err    error
	tokens int
	// asked records the last request, for the one test that is genuinely about
	// the prompt rather than the output.
	asked alchemy.LLMRequest
}

func (f *fakeLLM) Name() string { return f.name }

func (f *fakeLLM) Complete(_ context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	f.asked = req
	if f.err != nil {
		return alchemy.LLMResponse{}, f.err
	}
	return alchemy.LLMResponse{Text: f.reply, Tokens: f.tokens}, nil
}

// refusingLLM fails if it is called at all. It exists so a test can state
// "no model was called" as a property of the result rather than of a counter.
type refusingLLM struct{ t *testing.T }

func (r *refusingLLM) Name() string { return "refusing" }

func (r *refusingLLM) Complete(context.Context, alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	r.t.Helper()
	r.t.Fatal("the model was called although the caller supplied a mapping")
	return alchemy.LLMResponse{}, nil
}

func guessFor(res Result, field, chosenAs string) (alchemy.Guess, bool) {
	for _, g := range res.Guesses {
		if g.Field == field && g.ChosenAs == chosenAs {
			return g, true
		}
	}
	return alchemy.Guess{}, false
}

func hasAll(got, want []string) bool {
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
