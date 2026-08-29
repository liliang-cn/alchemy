package extract

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func TestRelationsReferToEntityIDsAndCarryProvenance(t *testing.T) {
	llm := &fakeLLM{name: "m", replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],
		     "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node","confidence":0.7}]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Relations) != 1 {
		t.Fatalf("Relations = %#v, want 1", got.Relations)
	}
	r := got.Relations[0]
	if r.From != "cluster:superai" || r.To != "node:node-a" || r.Type != "DEPLOYED_ON" {
		t.Errorf("relation = %q -[%s]-> %q", r.From, r.Type, r.To)
	}
	want := alchemy.Provenance{
		Source: "architecture.md", Chunk: 0, Producer: alchemy.ProducerLLMExtract,
		Model: "m", Ontology: "sds@3", Chunking: "heading", Confidence: 0.7,
	}
	if r.Provenance != want {
		t.Errorf("provenance =\n%#v\nwant\n%#v", r.Provenance, want)
	}
	// The ends must be IDs the entity list actually contains, or the graph is
	// a set of nodes and a set of edges that never meet.
	byID := map[string]bool{}
	for _, e := range got.Entities {
		byID[e.ID] = true
	}
	if !byID[r.From] || !byID[r.To] {
		t.Errorf("relation ends %q/%q are not entity IDs: %#v", r.From, r.To, got.Entities)
	}
}

// The model names SuperAI in chunk 1's relation without listing it as an entity
// there — it already did so in chunk 0. Overlap and re-mention are the normal
// case, not the exception, and an edge that failed to join across chunks is an
// edge that silently disappears at verification.
func TestARelationEndNotListedInItsOwnReplyStillJoins(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI"}],"relations":[]}`,
		1: `{"entities":[{"type":"Node","name":"node-a"}],
		     "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a", "b"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Relations) != 1 || got.Relations[0].From != "cluster:superai" {
		t.Fatalf("relations = %#v", got.Relations)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("entities = %#v, want the two named across the two chunks", got.Entities)
	}
}

// A model that gives an end no type is common. When exactly one extracted
// entity carries that name, the end is that entity and saying otherwise would
// break an edge over a field the model merely omitted.
func TestAnUntypedRelationEndResolvesByNameWhenUnambiguous(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],
		     "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","to":"node-a"}]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Relations) != 1 {
		t.Fatalf("relations = %#v", got.Relations)
	}
	if got.Relations[0].From != "cluster:superai" || got.Relations[0].To != "node:node-a" {
		t.Errorf("ends = %q/%q", got.Relations[0].From, got.Relations[0].To)
	}
}

// Where the name is ambiguous or matches nothing, the end is left unresolved
// rather than guessed. An unresolved end is a dangling relation, which the
// verifier reports (alchemy.ViolationDanglingRelation); a guessed one is an
// edge that points at the wrong node and looks exactly like a right one.
func TestAnUnresolvableRelationEndIsLeftDanglingRatherThanGuessed(t *testing.T) {
	t.Run("names nothing", func(t *testing.T) {
		llm := &fakeLLM{replies: map[int]string{
			0: `{"entities":[{"type":"Cluster","name":"SuperAI"}],
			     "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","to":"node-ghost"}]}`,
		}}
		got, err := Extract(context.Background(), testChunks("a"), testOptions(llm))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if len(got.Relations) != 1 {
			t.Fatalf("relations = %#v; the relation must survive so the verifier can report it", got.Relations)
		}
		for _, e := range got.Entities {
			if e.ID == got.Relations[0].To {
				t.Fatalf("extract invented an entity %#v for an end the model never declared", e)
			}
		}
	})
	t.Run("names two things", func(t *testing.T) {
		llm := &fakeLLM{replies: map[int]string{
			0: `{"entities":[{"type":"Person","name":"Mercury"},{"type":"Node","name":"Mercury"},{"type":"Cluster","name":"SuperAI"}],
			     "relations":[{"type":"MENTIONS","from":"SuperAI","to":"Mercury"}]}`,
		}}
		got, err := Extract(context.Background(), testChunks("a"), testOptions(llm))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		to := got.Relations[0].To
		if to == "person:mercury" || to == "node:mercury" {
			t.Errorf("To = %q: the name was ambiguous and extract picked one anyway", to)
		}
	})
}

// The same edge asserted by two chunks is one edge, for the same reason the
// same entity named by two chunks is one node.
func TestTheSameRelationInTwoChunksIsOneRelation(t *testing.T) {
	same := `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],
	          "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`
	llm := &fakeLLM{replies: map[int]string{0: same, 1: same}}
	got, err := Extract(context.Background(), testChunks("a", "b"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Relations) != 1 {
		t.Fatalf("relations = %#v, want 1", got.Relations)
	}
	if got.Relations[0].Provenance.Chunk != 0 {
		t.Errorf("provenance chunk = %d, want the earliest chunk that asserted it", got.Relations[0].Provenance.Chunk)
	}
}

// A chunk that yields only relations has not been read and found empty.
func TestAChunkYieldingOnlyRelationsIsNotCountedEmpty(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],"relations":[]}`,
		1: `{"entities":[],"relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a", "b"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.ChunksEmpty != 0 {
		t.Errorf("ChunksEmpty = %d, want 0", got.ChunksEmpty)
	}
}

// Some models write a relation end as an object rather than a bare name. It is
// the same claim, and a parser that accepts only one spelling turns a reply
// that was perfectly clear into an unread chunk.
func TestARelationEndMayBeAnObject(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],
		     "relations":[{"type":"DEPLOYED_ON","from":{"name":"SuperAI","type":"Cluster"},"to":{"name":"node-a","type":"Node"}}]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Relations) != 1 {
		t.Fatalf("relations = %#v", got.Relations)
	}
	if got.Relations[0].From != "cluster:superai" || got.Relations[0].To != "node:node-a" {
		t.Errorf("ends = %q/%q", got.Relations[0].From, got.Relations[0].To)
	}
}
