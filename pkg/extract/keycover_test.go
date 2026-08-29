package extract

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// vocabOf is a vocabulary declaring exactly the entity types named, which is
// all these tests need: what matters is that two vocabularies differ.
func vocabOf(t *testing.T, names ...string) ontology.Vocabulary {
	t.Helper()
	v := ontology.Vocabulary{}
	for _, n := range names {
		v.Entities = append(v.Entities, ontology.EntityType{Name: n})
	}
	return v
}

func withHeading(c alchemy.Chunk, h string) alchemy.Chunk { c.Heading = h; return c }
func withSource(c alchemy.Chunk, s string) alchemy.Chunk  { c.Source = s; return c }
func withIndex(c alchemy.Chunk, i int) alchemy.Chunk      { c.Index = i; return c }

// §8.2: "The cache is keyed on everything that would change the answer." Two
// runs that ask the model a different question must not share an address, and
// the vocabulary is the largest part of the question — it is pasted into the
// system prompt whole (§5b's "the same list on both sides of the model").
//
// The case that made this reachable: an ontology declares a prose part and a
// schema part, and a job now says which one it is extracted under. Same
// ontology ID, same model, same chunk text, different vocabulary — under a key
// that names only the ontology ID, the second job is served the first's answer,
// extracted under a vocabulary it never asked for and would have rejected.
func TestADifferentVocabularyIsADifferentQuestionAndADifferentAddress(t *testing.T) {
	c := alchemy.Chunk{Index: 0, Source: "a.md", Text: "SuperAI runs on node-a.", Strategy: "heading"}
	llm := &fakeLLM{name: "m"}

	prose := keyFor(c, Options{LLM: llm, OntologyID: "sds@3", Vocabulary: vocabOf(t, "Cluster", "Node")})
	schema := keyFor(c, Options{LLM: llm, OntologyID: "sds@3", Vocabulary: vocabOf(t, "Table", "Column")})

	if prose.Address() == schema.Address() {
		t.Fatalf("two vocabularies share one address %s: the second job is served the first's answer", prose.Address()[:16])
	}
}

// The user prompt names the source, the chunk index and the heading, so two
// chunks with identical text under different headings ask different questions.
// Sharing an address between them is the same failure one level down.
func TestADifferentPromptIsADifferentAddress(t *testing.T) {
	v := vocabOf(t, "Cluster", "Node")
	llm := &fakeLLM{name: "m"}
	base := alchemy.Chunk{Index: 0, Source: "a.md", Text: "It runs on node-a.", Strategy: "heading", Heading: "Deployment"}

	seen := map[string]string{}
	for _, tc := range []struct {
		name  string
		chunk alchemy.Chunk
	}{
		{"the chunk itself", base},
		{"a different heading", withHeading(base, "Storage")},
		{"a different source", withSource(base, "b.md")},
		{"a different index", withIndex(base, 7)},
	} {
		addr := keyFor(tc.chunk, Options{LLM: llm, OntologyID: "sds@3", Vocabulary: v}).Address()
		if prev, ok := seen[addr]; ok {
			t.Errorf("%q shares an address with %q, but they ask the model different questions", tc.name, prev)
		}
		seen[addr] = tc.name
	}
}

// The control: the same question really does hit. Without this the two tests
// above would pass on a key that hashed the current time.
func TestTheSameQuestionIsTheSameAddress(t *testing.T) {
	v := vocabOf(t, "Cluster", "Node")
	llm := &fakeLLM{name: "m"}
	c := alchemy.Chunk{Index: 3, Source: "a.md", Text: "SuperAI runs on node-a.", Strategy: "heading", Heading: "Deployment"}
	o := Options{LLM: llm, OntologyID: "sds@3", Vocabulary: v}

	if keyFor(c, o).Address() != keyFor(c, o).Address() {
		t.Fatal("the same chunk under the same options addressed differently twice")
	}
}

// And the whole point, end to end: a second run under a different vocabulary
// pays for the model again rather than being handed the first run's graph.
func TestASecondRunUnderADifferentVocabularyBuysItsOwnAnswer(t *testing.T) {
	store := cache.NewMemory(64)
	chunks := []alchemy.Chunk{{Index: 0, Source: "a.md", Text: "SuperAI runs on node-a.", Strategy: "heading"}}

	first := &fakeLLM{name: "m", replies: map[int]string{0: `{"entities":[{"type":"Cluster","name":"SuperAI"}],"relations":[]}`}}
	if _, err := Extract(context.Background(), chunks, Options{
		LLM: first, OntologyID: "sds@3", Vocabulary: vocabOf(t, "Cluster", "Node"), Cache: store,
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	second := &fakeLLM{name: "m", replies: map[int]string{0: `{"entities":[{"type":"Table","name":"SuperAI"}],"relations":[]}`}}
	res, err := Extract(context.Background(), chunks, Options{
		LLM: second, OntologyID: "sds@3", Vocabulary: vocabOf(t, "Table", "Column"), Cache: store,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.prompts) == 0 {
		t.Fatal("the schema job was served the prose job's answer without asking its own model")
	}
	if len(res.Entities) != 1 || res.Entities[0].Type != "Table" {
		t.Fatalf("entities = %+v, want the type the schema vocabulary declares", res.Entities)
	}
}
