package extract

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The same thing named in two chunks is one node. A graph in which chunk 0's
// SuperAI and chunk 40's SuperAI are two nodes is not a graph of the corpus, it
// is a graph of the chunking, and every traversal through it is half a
// traversal.
func TestTheSameEntityInTwoChunksIsOneEntity(t *testing.T) {
	llm := &fakeLLM{name: "m", replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"},"confidence":0.9}],"relations":[]}`,
		// A different spelling of the same name, and a different attribute.
		1: `{"entities":[{"type":"Cluster","name":"superai","attributes":{"tier":"gold"},"confidence":0.4}],"relations":[]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a", "b"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("want 1 merged entity, got %d: %#v", len(got.Entities), got.Entities)
	}
	e := got.Entities[0]
	if e.ID != "cluster:superai" {
		t.Errorf("ID = %q, want the folded type:name key", e.ID)
	}
	// The spelling shown is the one the document used first, not the folded key.
	if e.Name != "SuperAI" || e.Type != "Cluster" {
		t.Errorf("entity = %q/%q, want the first chunk's spelling", e.Type, e.Name)
	}
	if e.Attributes["region"] != "eu" || e.Attributes["tier"] != "gold" {
		t.Errorf("attributes = %#v, want the union of both chunks", e.Attributes)
	}
	// The whole provenance comes from one chunk, never assembled from pieces of
	// two: chunk 0's index with chunk 1's confidence would describe no reply
	// that any model actually gave.
	if e.Provenance.Chunk != 0 || e.Provenance.Confidence != 0.9 {
		t.Errorf("provenance = %#v, want chunk 0 and its own confidence 0.9", e.Provenance)
	}
}

// Two different types that happen to share a name are two things. Folding the
// name without the type would merge the Person Mercury into the Node Mercury.
func TestSameNameUnderDifferentTypesStaysTwoEntities(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Person","name":"Mercury"},{"type":"Node","name":"Mercury"}],"relations":[]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("want 2 entities, got %#v", got.Entities)
	}
	if got.Entities[0].ID == got.Entities[1].ID {
		t.Errorf("both entities share the ID %q", got.Entities[0].ID)
	}
}

var _ = alchemy.Entity{}

// Merging is the only place in the pipeline where two chunks disagreeing about
// one attribute can be made to vanish, because after the merge there is one
// node and the loser's value is gone. §7.3 makes a conflict the one thing a
// caller may not opt out of a person seeing, so it is reported here or nowhere.
func TestTwoChunksDisagreeingAboutAnAttributeIsAConflict(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"}}],"relations":[]}`,
		1: `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"us"}}],"relations":[]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a", "b"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Conflicts) != 1 {
		t.Fatalf("Conflicts = %#v, want the region disagreement", got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Kind != alchemy.ConflictEntityAttributes {
		t.Errorf("Kind = %q", c.Kind)
	}
	if c.Subject != "cluster:superai" {
		t.Errorf("Subject = %q, want the entity the two chunks disagree about", c.Subject)
	}
	// Each side carries its own chunk, or a reviewer cannot open either.
	if c.Left.Provenance.Chunk != 0 || c.Right.Provenance.Chunk != 1 {
		t.Errorf("sides = chunk %d and chunk %d, want 0 and 1", c.Left.Provenance.Chunk, c.Right.Provenance.Chunk)
	}
	if !strings.Contains(c.Left.Statement, "eu") || !strings.Contains(c.Right.Statement, "us") {
		t.Errorf("statements do not carry the two values: %q / %q", c.Left.Statement, c.Right.Statement)
	}
	// The merged entity keeps one value; which one is not the point, that a
	// person is told the other existed is.
	if got.Entities[0].Attributes["region"] != "eu" {
		t.Errorf("attributes = %#v, want the earliest chunk's value kept", got.Entities[0].Attributes)
	}
}

// Two chunks agreeing is not a disagreement.
func TestAgreeingAttributesAreNotAConflict(t *testing.T) {
	llm := &fakeLLM{replies: map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"}}],"relations":[]}`,
		1: `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"}}],"relations":[]}`,
	}}
	got, err := Extract(context.Background(), testChunks("a", "b"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.Conflicts) != 0 {
		t.Errorf("Conflicts = %#v, want none", got.Conflicts)
	}
}
