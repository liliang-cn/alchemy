package verify_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

func TestCountsReportTheGraphAndItsFaults(t *testing.T) {
	ddl := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	pdf := alchemy.Provenance{Source: "architecture.pdf", Chunk: 4, Producer: alchemy.ProducerLLMExtract}

	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "Cluster", Name: "prod", Provenance: ddl},
			{ID: "n1", Type: "Node", Name: "node-1", Provenance: ddl},
			{ID: "x1", Type: "Wormhole", Name: "w", Provenance: pdf},
		},
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Provenance: ddl},
			{From: "c1", To: "n1", Type: "MENTIONS", Provenance: pdf},
			{From: "c1", To: "n1", Type: "TELEPORTS_TO", Provenance: pdf},
		},
		Vocabulary: vocab(),
		OntologyID: "sds@3",
	})

	want := alchemy.Counts{
		Entities: 3, Relations: 3,
		Deterministic: 1, Inferred: 2,
		Violations: 2, Conflicts: 0,
	}
	if got.Counts != want {
		t.Fatalf("counts = %+v, want %+v", got.Counts, want)
	}
}

// §5: the counts are the obligation that justifies the scope, so a graph whose
// own numbers do not add up is worse than one with no numbers. The split is
// over relations — §5b's example reads 890 + 290 = 1180 edges — and it must
// hold for every producer, including ones added after this test was written.
func TestDeterministicPlusInferredAlwaysSumsToRelations(t *testing.T) {
	producers := []alchemy.Producer{
		alchemy.ProducerDDL, alchemy.ProducerGraphImport,
		alchemy.ProducerTabular, alchemy.ProducerLLMExtract,
		alchemy.Producer("something-invented-next-year"), alchemy.Producer(""),
	}
	var rels []alchemy.Relation
	for i, p := range producers {
		_ = i
		rels = append(rels, alchemy.Relation{From: "c1", To: "n1", Type: "MENTIONS", Provenance: alchemy.Provenance{Producer: p}})
	}
	got := verify.Check(graph(rels...))

	c := got.Counts
	if c.Deterministic+c.Inferred != c.Relations {
		t.Fatalf("deterministic %d + inferred %d != relations %d", c.Deterministic, c.Inferred, c.Relations)
	}
	if c.Relations != len(producers) {
		t.Fatalf("relations = %d, want %d", c.Relations, len(producers))
	}
	if c.Deterministic != 2 {
		t.Fatalf("deterministic = %d, want the two producers that read a statement", c.Deterministic)
	}
}

// Guesses, ChunksEmpty and ChunksUnread belong to stages this one cannot see.
// Reporting a computed-looking zero for them is the point: it is the same zero
// the field would carry if this stage did not touch it.
func TestCountsThisStageCannotComputeAreLeftZero(t *testing.T) {
	got := verify.Check(graph(alchemy.Relation{From: "c1", To: "n1", Type: "CONTAINS"}))
	if got.Counts.Guesses != 0 || got.Counts.ChunksEmpty != 0 || got.Counts.ChunksUnread != 0 {
		t.Fatalf("counts = %+v, want zero for the fields other stages own", got.Counts)
	}
}
