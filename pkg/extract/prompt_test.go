package extract

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// The vocabulary must reach the model as the ontology package wrote it. If this
// package ever paraphrases the type list, §5b's "same list on both sides of the
// model" is gone and nothing else in the pipeline would notice.
func TestSystemPromptCarriesTheVocabularyVerbatim(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{0: `{"entities":[],"relations":[]}`}}
	if _, err := Extract(context.Background(), testChunks("text"), testOptions(llm)); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	reqs := llm.requests()
	if len(reqs) == 0 {
		t.Fatal("the model was never called")
	}
	want := testVocab().Prompt()
	if !strings.Contains(reqs[0].System, want) {
		t.Errorf("system prompt does not contain Vocabulary.Prompt() verbatim.\ngot:\n%s\nwant to contain:\n%s",
			reqs[0].System, want)
	}
	if !reqs[0].JSON {
		t.Error("LLMRequest.JSON is false; the extractor asks for JSON and must say so")
	}
}

// A vocabulary the extractor is not allowed to widen: a type declared for a
// different part must never appear in the prompt. This is the structural half
// of §2.1's third lesson — the code vocabulary cannot leak into a prose run.
func TestSystemPromptContainsOnlyTheGivenVocabularysTypes(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{0: `{"entities":[],"relations":[]}`}}
	opts := testOptions(llm)
	if _, err := Extract(context.Background(), testChunks("text"), opts); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	sys := llm.requests()[0].System
	for _, undeclared := range []string{"function", "calls", "Table", "COLUMN_OF"} {
		if strings.Contains(sys, undeclared) {
			t.Errorf("system prompt mentions %q, which this vocabulary does not declare", undeclared)
		}
	}
}

func TestUserPromptCarriesTheChunkAndItsHeading(t *testing.T) {
	c := alchemy.Chunk{Index: 7, Text: "SuperAI runs on node-a.", Source: "a.md", Strategy: "heading", Heading: "Deployment"}
	llm := &fakeLLM{replies: map[int]string{7: `{"entities":[],"relations":[]}`}}
	if _, err := Extract(context.Background(), []alchemy.Chunk{c}, testOptions(llm)); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	p := llm.requests()[0].Prompt
	if !strings.Contains(p, c.Text) {
		t.Errorf("prompt does not contain the chunk text:\n%s", p)
	}
	if !strings.Contains(p, "Deployment") {
		t.Errorf("prompt drops the heading, so the model reads the section blind:\n%s", p)
	}
}

// An empty vocabulary forbids everything and permits nothing. §5 says there is
// no unconstrained mode, so this must be refused before a single call is paid
// for, not discovered as a run that returns nothing.
func TestExtractRefusesAnEmptyVocabulary(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{0: `{"entities":[],"relations":[]}`}}
	_, err := Extract(context.Background(), testChunks("text"), Options{LLM: llm, OntologyID: "sds@3"})
	if err == nil {
		t.Fatal("want an error for a vocabulary that declares no types, got nil")
	}
	if len(llm.requests()) != 0 {
		t.Error("the model was called under a vocabulary that constrains nothing")
	}
}

func TestExtractRefusesAMissingModelOrOntologyID(t *testing.T) {
	v := testVocab()
	t.Run("no llm", func(t *testing.T) {
		if _, err := Extract(context.Background(), testChunks("t"), Options{Vocabulary: v, OntologyID: "sds@3"}); err == nil {
			t.Fatal("want an error when Options.LLM is nil, got nil")
		}
	})
	t.Run("no ontology id", func(t *testing.T) {
		llm := &fakeLLM{replies: map[int]string{0: `{"entities":[],"relations":[]}`}}
		if _, err := Extract(context.Background(), testChunks("t"), Options{LLM: llm, Vocabulary: v}); err == nil {
			t.Fatal("want an error when OntologyID is empty: provenance that cannot name the vocabulary is not provenance")
		}
	})
}

var _ = ontology.Vocabulary{}
