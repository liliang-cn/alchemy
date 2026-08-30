package neo4j

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// pack is the fixture with its corpus made explicit: two sources, and the
// architecture.pdf chunk parameterised so that two imports of the same file can
// differ in exactly the way two imports of the same file do.
func pack(text string, start, end int) alchemy.Result {
	res := fixture()
	res.Chunks = []alchemy.Chunk{
		{Index: 14, Text: text, Source: "architecture.pdf", Strategy: "heading", Heading: "Storage", Start: start, End: end},
		{Index: 0, Text: "CREATE TABLE systems (id text);", Source: "schema.sql", Strategy: "ddl", Start: 0, End: 31},
	}
	return res
}

// This is the bug the interface exists to make impossible, written down.
//
// A store keeps every load, so a corpus imported twice is in it twice.
// northgate-profile.pdf was present under an old import with no byte offsets and
// under the current one; a citation lookup written without a load filter
// resolved against the old one and returned its text under a claim extracted
// from the new one, and nothing about the answer looked wrong.
func TestACitationResolvesInTheImportItWasAskedForAndNotTheOtherOneOfTheSameFile(t *testing.T) {
	l := liveLoader(t, Options{})
	ctx := context.Background()

	// The old import: the same file, the same chunk index, the text as it read
	// then, and no byte offsets — which is the shape of the load that caused
	// this, and the reason Start and End are on a Citation at all.
	l.opts.RunID = "recall-old"
	if _, err := l.Load(ctx, pack("An older draft said something else entirely.", 0, 0)); err != nil {
		t.Fatalf("loading the old import: %v", err)
	}
	l.opts.RunID = "recall-current"
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122)); err != nil {
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
	l := liveLoader(t, Options{RunID: "recall-miss"})
	ctx := context.Background()
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122)); err != nil {
		t.Fatalf("load: %v", err)
	}

	// A chunk this load does not hold: the claim cannot be checked here.
	_, err := l.Cite(ctx, "recall-miss", "architecture.pdf", 99)
	if !errors.Is(err, recall.ErrNoCitation) {
		t.Errorf("Cite of a missing chunk = %v, want ErrNoCitation", err)
	}
	// The right number against the wrong file. It would resolve on the index
	// alone, because a job's chunk indexes are unique across the whole job.
	if _, err := l.Cite(ctx, "recall-miss", "schema.sql", 14); !errors.Is(err, recall.ErrNoCitation) {
		t.Errorf("Cite of the right chunk under the wrong source = %v, want ErrNoCitation", err)
	}
	// A load that is not here at all, which is the caller naming the wrong
	// import — the bug the load parameter exists for, arriving as a typo.
	if _, err := l.Cite(ctx, "recall-typo", "architecture.pdf", 14); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite in an unknown load = %v, want ErrNoLoad", err)
	}
}

// The four questions, in the order an agent asks them, against a graph this
// package loaded rather than against a graph a test built by hand.
func TestTheFourQuestionsBuildAContextPack(t *testing.T) {
	l := liveLoader(t, Options{RunID: "recall-pack"})
	ctx := context.Background()
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122)); err != nil {
		t.Fatalf("load: %v", err)
	}

	// 1. Where the question enters the graph. Case-insensitively, because a
	// person types what they remember rather than what the corpus wrote.
	anchors, err := l.Find(ctx, "recall-pack", "cortex", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(anchors.Nodes) != 1 || anchors.Nodes[0].ID != "e2" || anchors.Nodes[0].Name != "CortexDB" || anchors.Nodes[0].Type != "System" {
		t.Fatalf("Find(cortex) = %+v, want just the CortexDB node with its id and type", anchors)
	}
	// The bookkeeping nodes carry the base label too, and a run marker, a chunk
	// and a finding are none of them anchors.
	if got, err := l.Find(ctx, "recall-pack", "", 100); err != nil || len(got.Nodes) != 3 {
		t.Fatalf("Find(everything) = %d nodes (%v), want the 3 entities and nothing this connector wrote for itself", len(got.Nodes), err)
	}

	// 2. One hop, each claim with the provenance of the edge.
	claims, err := l.Claims(ctx, "recall-pack", "e1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	want := []string{
		"SuperAI -[USES]-> CortexDB (inferred, llm-extract) [architecture.pdf#14]",
		"Ada -[WORKS_ON]-> SuperAI (stated, ddl) [schema.sql]",
	}
	if len(claims) != len(want) {
		t.Fatalf("Claims(e1) = %v, want exactly the two extracted edges — a CANDIDATE and a REPLACED_BY also touch e1", claims)
	}
	for i, w := range want {
		if got := claims[i].String(); got != w {
			// WORKS_ON is the row that catches a walk reading the subject
			// node's provenance instead of the edge's: Ada was named by the
			// model in architecture.pdf, and the edge was stated by a foreign
			// key in schema.sql.
			t.Errorf("claim %d = %q, want %q", i, got, w)
		}
	}

	// 3. The citation the first claim carries, resolved.
	cited, err := l.Cite(ctx, "recall-pack", claims[0].Source, claims[0].Chunk)
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if cited.Text != "SuperAI uses CortexDB." || cited.Start != 100 || cited.End != 122 {
		t.Errorf("Cite = %+v, want the sentence the claim was extracted from and where it is in the file", cited)
	}

	// 4. What nobody has decided. findings.go deliberately does not make this
	// an edge between the two nodes, so this is the only way to be told.
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

// A load that is still arriving must answer nothing. pgvector gets this from
// its loaded_* views; here it is the run marker, and nothing consulted it
// before there was a read path.
func TestALoadThatHasNotFinishedAnswersNothing(t *testing.T) {
	l := liveLoader(t, Options{RunID: "recall-partial"})
	ctx := context.Background()
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122)); err != nil {
		t.Fatalf("load: %v", err)
	}
	l.mustQuery(t, "MATCH (r:"+mustQuote(t, l.internalLabel("Run"))+" {`_id`: $run}) SET r.`_complete` = false",
		map[string]any{"run": "recall-partial"})

	if got, err := l.Find(ctx, "recall-partial", "cortex", 10); err != nil || len(got.Nodes) != 0 {
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
