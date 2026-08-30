package pgvector

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// pack is a small multi-source result: a model reading prose and a foreign key
// reading a schema, so that a claim's provenance and its subject's provenance
// are different files and reading the wrong one is visible.
//
// The architecture.pdf chunk is parameterised so that two imports of one file
// can differ in exactly the way two imports of one file do.
func pack(text string, start, end int) alchemy.Result {
	llm := prov(14)
	ddl := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "Service", Name: "SuperAI", Provenance: llm},
			{ID: "e2", Type: "Store", Name: "CortexDB", Provenance: ddl},
			{ID: "e3", Type: "Person", Name: "Ada", Provenance: llm},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Provenance: llm},
			{From: "e3", To: "e1", Type: "WORKS_ON", Provenance: ddl},
			// An edge naming an entity this result does not contain. §7.3
			// delivers that rather than refusing it, and there is no foreign
			// key here on purpose, so it is a row a walk has to render.
			{From: "e1", To: "e-not-here", Type: "MENTIONS", Provenance: llm},
		},
		Chunks: []alchemy.Chunk{
			{Index: 14, Text: text, Source: "architecture.pdf", Strategy: "semantic", Heading: "Storage", Start: start, End: end},
			{Index: 0, Text: "CREATE TABLE systems (id text);", Source: "schema.sql", Strategy: "ddl", Start: 0, End: 31},
		},
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "CortexDB ~ SuperAI",
			Detail: "one name is the other with a word added",
			Left:   alchemy.DuplicateSide{ID: "e2", Type: "Store", Name: "CortexDB", Provenance: ddl},
			Right:  alchemy.DuplicateSide{ID: "e1", Type: "Service", Name: "SuperAI", Provenance: llm},
		}},
		Counts: alchemy.Counts{Entities: 3, Relations: 3, Deterministic: 2, Inferred: 4, Duplicates: 1},
	}
}

// This is the bug the interface exists to make impossible, written down.
//
// A store keeps every load, so a corpus imported twice is in it twice. The file
// that caused this was present under an old import with no byte offsets and
// under the current one, and a citation lookup without a load filter resolved
// against the old one and returned its text under a claim extracted from the
// new one. Nothing about the answer looked wrong.
func TestACitationResolvesInTheImportItWasAskedForAndNotTheOtherOneOfTheSameFile(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()

	if _, err := l.Load(ctx, pack("An older draft said something else entirely.", 0, 0),
		LoadOptions{ID: "recall-old"}); err != nil {
		t.Fatalf("loading the old import: %v", err)
	}
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122),
		LoadOptions{ID: "recall-current"}); err != nil {
		t.Fatalf("loading the current import: %v", err)
	}

	for _, tc := range []struct {
		load, text string
		start, end int
	}{
		{"recall-current", "SuperAI uses CortexDB.", 100, 122},
		{"recall-old", "An older draft said something else entirely.", 0, 0},
	} {
		got, err := l.Cite(ctx, tc.load, "architecture.pdf", 14)
		if err != nil {
			t.Fatalf("Cite in %s: %v", tc.load, err)
		}
		if got.Text != tc.text {
			t.Errorf("Cite(%q).Text = %q, want %q: the citation resolved against the other import of the same file",
				tc.load, got.Text, tc.text)
		}
		if got.Start != tc.start || got.End != tc.end {
			t.Errorf("Cite(%q) span = %d..%d, want %d..%d", tc.load, got.Start, got.End, tc.start, tc.end)
		}
		if got.Source != "architecture.pdf" || got.Index != 14 {
			t.Errorf("Cite(%q) = %+v, want the marker it was asked for", tc.load, got)
		}
	}

	// The other source of the same load, so that "the right file" is being
	// tested and not "the only file".
	if got, err := l.Cite(ctx, "recall-current", "schema.sql", 0); err != nil || got.Text != "CREATE TABLE systems (id text);" {
		t.Errorf("Cite(schema.sql#0) = %+v, %v; a load holds more than one source", got, err)
	}
}

// An agent handed an empty citation treats the claim as
// unsupported-but-plausible. One told the citation does not resolve knows not
// to offer it as evidence, and the two absences have different fixes.
func TestACitationThatDoesNotResolveSaysSoAndSaysWhich(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122), LoadOptions{ID: "recall-miss"}); err != nil {
		t.Fatalf("load: %v", err)
	}

	if _, err := l.Cite(ctx, "recall-miss", "architecture.pdf", 99); !errors.Is(err, recall.ErrNoCitation) {
		t.Errorf("Cite of a missing chunk = %v, want ErrNoCitation", err)
	}
	// The right number against the wrong file. It would resolve on the index
	// alone, because a job's chunk indexes are unique across the whole job.
	if _, err := l.Cite(ctx, "recall-miss", "schema.sql", 14); !errors.Is(err, recall.ErrNoCitation) {
		t.Errorf("Cite of the right chunk under the wrong source = %v, want ErrNoCitation", err)
	}
	if _, err := l.Cite(ctx, "recall-typo", "architecture.pdf", 14); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite in an unknown load = %v, want ErrNoLoad", err)
	}
}

// The four questions, in the order an agent asks them.
func TestTheFourQuestionsBuildAContextPack(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122), LoadOptions{ID: "recall-pack"}); err != nil {
		t.Fatalf("load: %v", err)
	}

	// 1. Where the question enters the graph, case-insensitively: a person
	// types what they remember rather than what the corpus wrote.
	anchors, err := l.Find(ctx, "recall-pack", "cortex", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(anchors) != 1 || anchors[0].ID != "e2" || anchors[0].Name != "CortexDB" || anchors[0].Type != "Store" {
		t.Fatalf("Find(cortex) = %+v, want just the CortexDB node with its id and type", anchors)
	}

	// 2. One hop, each claim with the provenance of the edge.
	claims, err := l.Claims(ctx, "recall-pack", "e1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	want := []string{
		// The dangling edge renders its endpoint by the id it named. There is
		// no entity row to join to, and a claim about something this load does
		// not describe is not the same as no claim.
		"SuperAI -[MENTIONS]-> e-not-here (inferred, llm-extract) [architecture.pdf#14]",
		"SuperAI -[USES]-> CortexDB (inferred, llm-extract) [architecture.pdf#14]",
		// The row that catches a walk reading the subject's provenance instead
		// of the edge's: Ada was named by the model in architecture.pdf, and
		// this edge was stated by a foreign key in schema.sql.
		"Ada -[WORKS_ON]-> SuperAI (stated, ddl) [schema.sql]",
	}
	if len(claims) != len(want) {
		t.Fatalf("Claims(e1) = %v, want %d", claims, len(want))
	}
	for i, w := range want {
		if got := claims[i].String(); got != w {
			t.Errorf("claim %d = %q, want %q", i, got, w)
		}
	}

	// 3. The citation the second claim carries, resolved.
	cited, err := l.Cite(ctx, "recall-pack", claims[1].Source, claims[1].Chunk)
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if cited.Text != "SuperAI uses CortexDB." || cited.Start != 100 || cited.End != 122 {
		t.Errorf("Cite = %+v, want the sentence the claim was extracted from and where it is in the file", cited)
	}

	// 4. What nobody has decided. The duplicates are a table rather than a blob
	// on the load precisely so that they can be asked about beside the records.
	open, err := l.Unanswered(ctx, "recall-pack", "cortexdb")
	if err != nil {
		t.Fatalf("Unanswered: %v", err)
	}
	if len(open) != 1 || open[0].Signal != alchemy.DuplicateNameAffix {
		t.Fatalf("Unanswered(cortexdb) = %+v, want the one identity question touching it", open)
	}
	if open[0].Left != "CortexDB" || open[0].Right != "SuperAI" || open[0].Detail == "" {
		t.Errorf("question = %+v, want both sides named and the case stated", open[0])
	}
	if all, err := l.Unanswered(ctx, "recall-pack", ""); err != nil || len(all) != 1 {
		t.Errorf("Unanswered(everything) = %d (%v), want 1: an empty subject is every question, not none", len(all), err)
	}
	if none, err := l.Unanswered(ctx, "recall-pack", "a subject nobody wrote about"); err != nil || len(none) != 0 {
		t.Errorf("Unanswered(absent subject) = %d (%v), want none", len(none), err)
	}
}

// A load that is still arriving must answer nothing. It is the loaded_* views
// that make that true here, and this is the assertion that they cover the read
// path as well as the write one.
func TestALoadThatHasNotFinishedAnswersNothing(t *testing.T) {
	fx := newFixture(t)
	l := fx.open(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122), LoadOptions{ID: "recall-partial"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := fx.admin.Exec(ctx, "UPDATE "+fx.schema+".loads SET state = '"+stateLoading+"' WHERE id = $1", "recall-partial"); err != nil {
		t.Fatalf("reopening the load: %v", err)
	}

	if got, err := l.Find(ctx, "recall-partial", "cortex", 10); err != nil || len(got) != 0 {
		t.Errorf("Find on an unfinished load = %+v (%v), want nothing", got, err)
	}
	if got, err := l.Claims(ctx, "recall-partial", "e1"); err != nil || len(got) != 0 {
		t.Errorf("Claims on an unfinished load = %+v (%v), want nothing", got, err)
	}
	if got, err := l.Unanswered(ctx, "recall-partial", ""); err != nil || len(got) != 0 {
		t.Errorf("Unanswered on an unfinished load = %+v (%v), want nothing", got, err)
	}
	if _, err := l.Cite(ctx, "recall-partial", "architecture.pdf", 14); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite on an unfinished load = %v, want ErrNoLoad: half a graph must not be cited as a whole one", err)
	}
}
