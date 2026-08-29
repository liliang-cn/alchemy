package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/chunk"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/source/tabular"
)

// mixedJob is one job of every kind at once: a schema, an imported graph, a
// document whose second section the endpoint refuses, and a table under a
// mapping the caller stated. It is the shape §5's obligation is about — a
// result assembled out of four readers and three stages, whose Counts have to
// describe the slices next to them and not the stages that produced them.
func mixedJob(t *testing.T) Request {
	t.Helper()
	llm := &scriptLLM{name: "gemini-3.6-flash-high", tokens: 11, replies: map[string]string{
		"eu-west": `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],
		             "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
	}, errs: map[string]error{
		"Beta": fmt.Errorf("the endpoint refused this chunk"),
	}}
	return Request{
		Sources: []Source{
			{Name: "schema.sql", Kind: alchemy.SourceDDL, Open: openString(twoTables)},
			{Name: "kg.json", Kind: alchemy.SourceGraph, Open: openString(knowledgeGraph)},
			{Name: "architecture.md", Kind: alchemy.SourceDocument,
				Open: openString("# Alpha\n\nSuperAI is the cluster in eu-west, on node-a.\n\n# Beta\n\nUnreadable.\n")},
			{Name: "orders.csv", Kind: alchemy.SourceTabular, Open: openString(ordersCSV)},
		},
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Chunking: chunk.Options{Overlap: chunk.NoOverlap},
		Models:   alchemy.Models{LLM: llm, Embedder: &fakeEmbedder{name: "fake-embed-3"}},
		Mapping: &tabular.Mapping{
			EntityType: "Order", IDColumn: "id",
			Attributes: map[string]string{"total": "total"},
			Relations:  []tabular.RelationMapping{{Column: "customer_id", RelationType: "PLACED_BY", TargetType: "Customer"}},
		},
	}
}

// §5: "Every returned graph is accompanied by the numbers needed to distrust
// it." A graph whose own numbers do not add up is worse than one with no
// numbers, so every field is checked against the slice it claims to count —
// the audit a reader would do by hand if they did not believe the block.
func TestCountsDescribeTheSlicesTheyAreNextTo(t *testing.T) {
	res, err := Run(context.Background(), mixedJob(t), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := res.Counts
	audit := []struct {
		field string
		got   int
		want  int
	}{
		{"Entities", c.Entities, len(res.Entities)},
		{"Relations", c.Relations, len(res.Relations)},
		{"Violations", c.Violations, len(res.Violations)},
		{"Conflicts", c.Conflicts, len(res.Conflicts)},
		{"Guesses", c.Guesses, len(res.Guesses)},
		{"ChunksUnread", c.ChunksUnread, len(res.Unread)},
	}
	for _, a := range audit {
		if a.got != a.want {
			t.Errorf("Counts.%s = %d, but the result carries %d", a.field, a.got, a.want)
		}
	}
	// The split §5b puts at the centre of the block: 890 + 290 = 1180.
	if c.Deterministic+c.Inferred != c.Relations {
		t.Errorf("Deterministic(%d) + Inferred(%d) = %d, want Relations(%d)",
			c.Deterministic, c.Inferred, c.Deterministic+c.Inferred, c.Relations)
	}
	deterministic := 0
	for _, rel := range res.Relations {
		if rel.Provenance.Producer.Deterministic() {
			deterministic++
		}
	}
	if c.Deterministic != deterministic {
		t.Errorf("Counts.Deterministic = %d, but %d relations carry a deterministic producer", c.Deterministic, deterministic)
	}
	// The job is built to exercise both halves, so a run where one of them is
	// empty is a run that proved nothing about the split.
	if deterministic == 0 || c.Inferred == 0 {
		t.Errorf("the mixed job produced %d deterministic and %d inferred relations; both halves are needed", deterministic, c.Inferred)
	}
	if len(res.Unread) == 0 {
		t.Error("the refused chunk is not in Unread; §5 forbids it being silently empty")
	}
}

// ChunksEmpty is the number §5b calls out as the sign that "the extraction is
// failing quietly": a chunk the model read and honestly found nothing in. It
// is summed across the stages that can tell, because they are the only two
// places a chunk can turn out to hold nothing.
func TestChunksThatProducedNothingAreCounted(t *testing.T) {
	llm := &scriptLLM{replies: map[string]string{
		"eu-west": `{"entities":[{"type":"Cluster","name":"SuperAI"}],"relations":[]}`,
	}}
	req := regionRequest(t, doc("architecture.md",
		"# Alpha\n\nSuperAI is the cluster in eu-west.\n\n# Beta\n\nThis section states nothing the vocabulary can express.\n"))
	req.Chunking.Overlap = chunk.NoOverlap
	req.Models.LLM = llm
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Counts.ChunksEmpty != 1 {
		t.Errorf("ChunksEmpty = %d, want 1: one of the two sections produced nothing", res.Counts.ChunksEmpty)
	}
}
