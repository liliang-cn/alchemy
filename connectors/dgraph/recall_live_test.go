package dgraph

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// fixture is a small result that exercises every shape this connector has an
// opinion about: two producers, an attribute map with a non-string value, a
// chunk with real offsets, an edge in each direction from one node, and a
// duplicate finding.
func fixture() alchemy.Result {
	llm := alchemy.Provenance{
		Source: "architecture.pdf", Chunk: 0, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading", Confidence: 0.82,
		ReviewedBy: "ada@example.com", RuleSet: "rs-9f21",
	}
	ddl := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI",
				Aliases: []string{"Super AI"}, Attributes: map[string]any{"public": true, "lang": "go"},
				Provenance: llm},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			{ID: "e3", Type: "Person", Name: "Ada", Provenance: llm},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Provenance: llm},
			{From: "e3", To: "e1", Type: "WORKS_ON", Provenance: ddl},
		},
		Chunks: []alchemy.Chunk{
			{Index: 0, Text: "SuperAI uses CortexDB.", Source: "architecture.pdf",
				Strategy: "heading", Heading: "Storage", Start: 100, End: 122},
		},
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "CortexDB ~ CortexDB store",
			Detail: "one name is the other with a word added",
			Left:   alchemy.DuplicateSide{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			Right:  alchemy.DuplicateSide{ID: "e1", Type: "System", Name: "SuperAI", Provenance: llm},
		}},
		Counts: alchemy.Counts{Entities: 3, Relations: 2, Deterministic: 2, Inferred: 3, Duplicates: 1},
	}
}

// loaded writes the fixture and a SECOND unrelated load through the same
// Loader, sharing a predicate prefix on purpose.
//
// The second load is what makes every assertion below mean something. A Dgraph
// alpha has one namespace for everything, so a read that forgot to scope by run
// would answer from both — and it would answer, not fail.
func loaded(t *testing.T, load string) *Loader {
	t.Helper()
	l := liveLoader(t, Options{RunID: load})
	ctx := context.Background()
	if _, err := sink.Load(ctx, l, fixture(), sink.Options{Load: load}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	other := "nb-" + randomName(t)
	p := alchemy.Provenance{Source: "other.csv", Chunk: -1, Producer: alchemy.ProducerTabular}
	neighbour := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "x1", Type: "System", Name: "SuperAI", Provenance: p},
			{ID: "x2", Type: "Robot", Name: "Ada", Provenance: p},
		},
		Counts: alchemy.Counts{Entities: 2, Deterministic: 2},
	}
	if _, err := sink.Load(ctx, l, neighbour, sink.Options{Load: other}); err != nil {
		t.Fatalf("Load neighbour: %v", err)
	}
	t.Cleanup(func() { _ = l.dropLoad(context.Background(), other) })
	return l
}

func TestFindReadsOneLoadOutOfOneCluster(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	load := l.opts.RunID
	got, err := l.Find(context.Background(), load, "superai", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	// The neighbour holds an entity of the same type with the same name.
	// One hit, and the fold is on both sides, so a lowercase needle found
	// "SuperAI".
	if got.Total != 1 || len(got.Nodes) != 1 || got.Nodes[0].ID != "e1" {
		t.Fatalf("Find = %+v, want exactly e1", got)
	}
	if got.Nodes[0].Type != "System" || got.Nodes[0].Name != "SuperAI" {
		t.Fatalf("anchor = %+v", got.Nodes[0])
	}
}

func TestFindSaysWhatThePageLeftOut(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	got, err := l.Find(context.Background(), l.opts.RunID, "", 2)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Total != 3 || len(got.Nodes) != 2 || !got.Truncated() {
		t.Fatalf("Find = %+v, want 2 of 3 and Truncated", got)
	}
	if got.Nodes[0].Name != "Ada" || got.Nodes[1].Name != "CortexDB" {
		t.Fatalf("page = %v, want the first two by name",
			[]string{got.Nodes[0].Name, got.Nodes[1].Name})
	}
}

func TestTypesGroupsInsideOneLoad(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	got, err := l.Types(context.Background(), l.opts.RunID)
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	want := map[string]int{"Person": 1, "System": 2}
	if len(got) != len(want) {
		t.Fatalf("Types = %+v, want %v", got, want)
	}
	for _, tc := range got {
		if want[tc.Type] != tc.Count {
			t.Errorf("Types says %d %s, want %d", tc.Count, tc.Type, want[tc.Type])
		}
		// The neighbour's Robot must not be here, and neither must the chunk
		// and marker nodes this connector writes under the same run: counting
		// nodes instead of entities reports a vocabulary no ontology declared.
		if tc.Type == "Robot" || tc.Type == "" {
			t.Errorf("Types reports %q, which is not an entity type of this load", tc.Type)
		}
	}
}

func TestOfTypeIsExactWhereFindIsNot(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	ctx := context.Background()
	got, err := l.OfType(ctx, l.opts.RunID, "System", 10)
	if err != nil {
		t.Fatalf("OfType: %v", err)
	}
	if got.Total != 2 {
		t.Fatalf("OfType(System) = %+v, want the 2 of this load", got)
	}
	lower, err := l.OfType(ctx, l.opts.RunID, "system", 10)
	if err != nil {
		t.Fatalf("OfType: %v", err)
	}
	if lower.Total != 0 {
		t.Fatalf("OfType(\"system\") matched %d; a fold here would report a vocabulary the load does not have",
			lower.Total)
	}
}

func TestClaimsCarryTheirOwnProvenanceInBothDirections(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	got, err := l.Claims(context.Background(), l.opts.RunID, "e1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	// e1 USES e2 outgoing and e3 WORKS_ON e1 incoming. Both in one answer,
	// which is what @reverse buys — and an incoming edge keeps the direction
	// the extractor wrote it in rather than being normalised.
	if len(got) != 2 {
		t.Fatalf("Claims about e1 = %d, want 2 — %+v", len(got), got)
	}
	by := map[string]recall.Claim{}
	for _, c := range got {
		by[c.Type] = c
	}
	uses, ok := by["USES"]
	if !ok {
		t.Fatalf("no USES claim in %+v", got)
	}
	if uses.FromID != "e1" || uses.ToID != "e2" || uses.From != "SuperAI" || uses.To != "CortexDB" {
		t.Errorf("USES = %+v, want e1/SuperAI -> e2/CortexDB", uses)
	}
	works, ok := by["WORKS_ON"]
	if !ok {
		t.Fatalf("no WORKS_ON claim in %+v", got)
	}
	if works.FromID != "e3" || works.ToID != "e1" {
		t.Errorf("WORKS_ON = %+v, want e3 -> e1: an incoming edge is the same assertion "+
			"read from the other end, not a reversed one", works)
	}
	// The edge's own provenance, not its subject's. e1's node came from the
	// llm-extract record and the WORKS_ON edge from the ddl one.
	if !works.Stated || works.Producer != alchemy.ProducerDDL {
		t.Errorf("WORKS_ON = %+v, want the edge's own ddl provenance", works)
	}
	if uses.Stated || uses.Producer != alchemy.ProducerLLMExtract || uses.Chunk != 0 {
		t.Errorf("USES = %+v, want the edge's own llm-extract provenance at chunk 0", uses)
	}
}

func TestDescribeReturnsTheRecordWithItsTypesIntact(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	d, err := l.Describe(context.Background(), l.opts.RunID, "e1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if d.ID != "e1" || d.Name != "SuperAI" || d.Type != "System" {
		t.Fatalf("Describe = %+v", d)
	}
	if len(d.Aliases) != 1 || d.Aliases[0] != "Super AI" {
		t.Errorf("Aliases = %v", d.Aliases)
	}
	if d.Attributes["public"] != true || d.Attributes["lang"] != "go" {
		t.Errorf("Attributes = %#v, want the boolean and the string the source stated", d.Attributes)
	}
	// The whole provenance, which is the only place these fields are reachable.
	if d.Provenance.Model != "gemini-3.6-flash-high" || d.Provenance.Confidence != 0.82 ||
		d.Provenance.ReviewedBy != "ada@example.com" || d.Provenance.RuleSet != "rs-9f21" {
		t.Errorf("Provenance = %+v, want the whole of it", d.Provenance)
	}
	// A record whose producer worked in no chunks says so, and does not cite
	// chunk 0 of its file.
	d2, err := l.Describe(context.Background(), l.opts.RunID, "e2")
	if err != nil {
		t.Fatalf("Describe e2: %v", err)
	}
	if d2.Provenance.Chunk != -1 {
		t.Errorf("e2 chunk = %d, want -1: its producer does not work in chunks", d2.Provenance.Chunk)
	}
}

func TestCiteWantsBothHalvesOfTheMarker(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	ctx := context.Background()
	c, err := l.Cite(ctx, l.opts.RunID, "architecture.pdf", 0)
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if c.Text != "SuperAI uses CortexDB." || c.Start != 100 || c.End != 122 {
		t.Fatalf("Cite = %+v", c)
	}
	if _, err := l.Cite(ctx, l.opts.RunID, "schema.sql", 0); !errors.Is(err, recall.ErrNoCitation) {
		t.Fatalf("Cite with the wrong source = %v, want ErrNoCitation", err)
	}
	if _, err := l.Cite(ctx, l.opts.RunID, "architecture.pdf", -1); !errors.Is(err, recall.ErrNoChunk) {
		t.Fatalf("Cite(-1) = %v, want ErrNoChunk", err)
	}
}

func TestUnansweredFiltersWithoutASentinel(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	ctx := context.Background()
	all, err := l.Unanswered(ctx, l.opts.RunID, "")
	if err != nil {
		t.Fatalf("Unanswered: %v", err)
	}
	if len(all) != 1 || all[0].Left != "CortexDB" || all[0].Right != "SuperAI" {
		t.Fatalf("Unanswered = %+v", all)
	}
	if all[0].Signal != alchemy.DuplicateNameAffix {
		t.Errorf("Signal = %q", all[0].Signal)
	}
	if lit, err := l.Unanswered(ctx, l.opts.RunID, "all"); err != nil || len(lit) != 0 {
		t.Fatalf("Unanswered(\"all\") = %v, %v; \"all\" is a search term here, not everything", lit, err)
	}
	if hit, err := l.Unanswered(ctx, l.opts.RunID, "cortexdb"); err != nil || len(hit) != 1 {
		t.Fatalf("Unanswered(\"cortexdb\") = %v, %v", hit, err)
	}
}

func TestContributionsSeeTheEdgesAsWellAsTheNode(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	got, err := l.Contributions(context.Background(), l.opts.RunID, "e1")
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	if got.ID != "e1" || got.Type != "System" {
		t.Fatalf("Contributions = %+v", got)
	}
	if !got.Joined() {
		t.Errorf("e1 has records from architecture.pdf and schema.sql; Joined() should say so: %+v", got)
	}
	named := 0
	for _, c := range got.Contributors {
		if c.Name != "" {
			named++
		}
	}
	// Only the node's own record carries a name. Copying it onto every
	// contributor would report that all of them agreed on it.
	if named != 1 {
		t.Errorf("%d contributors carry a name, want exactly the node's own record: %+v", named, got.Contributors)
	}
}

func TestEveryReadRefusesALoadThatIsNotFinished(t *testing.T) {
	l := loaded(t, "ld-"+randomName(t))
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"Describe":      func() error { _, err := l.Describe(ctx, "never-ran", "e1"); return err },
		"Contributions": func() error { _, err := l.Contributions(ctx, "never-ran", "e1"); return err },
		"Cite":          func() error { _, err := l.Cite(ctx, "never-ran", "architecture.pdf", 0); return err },
	} {
		if err := call(); !errors.Is(err, recall.ErrNoLoad) {
			t.Errorf("%s on an unknown load = %v, want ErrNoLoad", name, err)
		}
	}
	if f, err := l.Find(ctx, "never-ran", "x", 5); err != nil || f.Total != 0 {
		t.Errorf("Find on an unknown load = %+v, %v", f, err)
	}
	if ts, err := l.Types(ctx, "never-ran"); err != nil || len(ts) != 0 {
		t.Errorf("Types on an unknown load = %v, %v", ts, err)
	}
}
