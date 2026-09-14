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

// TestThePromptAsksForWhatTheVocabularyCannotSay is the line that made every
// mechanism downstream of it unreachable.
//
// The prompt used to end an unexpressible chunk with "An empty answer is a
// correct answer here". Under that instruction a model reading "Christoph,
// Joel C, Lars and Philipp are on the DRBD team" against a vocabulary with no
// membership relation returns five entities and no relations — and because it
// emitted no relation, verify has nothing to call undeclared, proposals is
// derived from violations and stays empty, and the propose → approve → extend
// workflow whose entire purpose is "the corpus needed a type you do not
// declare" can only ever fire when the model disobeys. Four people land in the
// graph connected to nothing, the counters all read zero, and the one thing the
// document said about them is gone with no trace.
//
// So the model is asked for it instead. What comes back is checked against the
// vocabulary like everything else: it is named as a violation, proposed as a
// type with the ends it was used between, and graded refused by a store rather
// than dropped. An over-eager model makes review work; a silent one makes a
// graph nobody can audit, and between those two this is the safe direction to
// be wrong in.
func TestThePromptAsksForWhatTheVocabularyCannotSay(t *testing.T) {
	got := systemPrompt(testVocab(), nil)

	if strings.Contains(got, "An empty answer is a correct answer") {
		t.Error("the prompt still tells the model that a chunk it cannot express is an empty chunk, " +
			"which is the instruction that makes a missing vocabulary word invisible")
	}
	for _, want := range []string{
		// It must say what to do instead, and say it about relations, which is
		// where the hole was demonstrated.
		"cannot express",
		"the type you would have declared",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt does not say %q, so a model has no way to report a word the vocabulary lacks:\n%s", want, got)
		}
	}
	// And it must still refuse invention. The closed vocabulary is why this
	// pipeline is worth anything; reporting a gap is not permission to fill it.
	if !strings.Contains(got, "states") {
		t.Error("the prompt no longer ties the report to what the chunk states, which turns a gap report into permission to invent")
	}
	// An empty chunk is still an empty answer — that is what keeps ChunksEmpty
	// a fact about the documents rather than about the vocabulary.
	if !strings.Contains(got, `{"entities": [], "relations": []}`) {
		t.Error("the prompt no longer gives a way to answer nothing, so a chunk with nothing in it has no correct reply")
	}
}

// TestThePromptAsksForAnAttributeTheTypeHasNoFieldFor is the third form.
//
// The shape asked for "<a declared attribute>" and the attribute list was
// shown to the model and checked by nobody, so a detail the chunk stated and
// the type had no field for was dropped by an obedient model and would have
// been written unremarked by a disobedient one. Measured: a vocabulary
// declaring City(name) and a document saying an office was established in 2008
// put no 2008 anywhere in the store and raised nothing.
func TestThePromptAsksForAnAttributeTheTypeHasNoFieldFor(t *testing.T) {
	got := systemPrompt(testVocab(), nil)
	for _, want := range []string{
		"has no attribute",
		"the name you would have declared",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt does not say %q, so a detail with no field to go in is still dropped in silence:\n%s", want, got)
		}
	}
}
