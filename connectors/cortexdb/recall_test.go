package cortexdb

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Reading a load back out of a brain that holds other things.
//
// Every test here loads the fixture and then loads a SECOND, unrelated batch
// under a different run, because that is the condition this store is under and
// the other three are not: CortexDB is shared, and a read that answered
// correctly against a database holding one import would be a read nobody had
// tested. Until v2.89.0 the store had no way to be asked about one batch at
// all, which is why this file did not exist.

// loaded opens a store, writes the fixture under load, and writes a second
// batch under "neighbour" that must never appear in any answer.
func loaded(t *testing.T, load string) *Loader {
	t.Helper()
	l := openLocal(t, Options{RunID: load})
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	other := openOn(t, l, "neighbour")
	if _, err := other.Load(context.Background(), neighbourResult()); err != nil {
		t.Fatalf("Load neighbour: %v", err)
	}
	return l
}

// openOn returns a second Loader over the same database under another run.
func openOn(t *testing.T, l *Loader, run string) *Loader {
	t.Helper()
	o := Options{RunID: run}.withDefaults()
	return &Loader{cortex: l.cortex, opts: o}
}

// neighbourResult is somebody else's import. It shares a type and a name with
// the fixture on purpose: a scope that worked by type or by name rather than by
// batch would pass every test in this file without it.
func neighbourResult() alchemy.Result {
	p := alchemy.Provenance{Source: "other.csv", Chunk: -1, Producer: alchemy.ProducerTabular, Ontology: "sds@3"}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "x1", Type: "System", Name: "SuperAI", Provenance: p},
			{ID: "x2", Type: "System", Name: "Elsewhere", Provenance: p},
			{ID: "x3", Type: "Robot", Name: "Ada", Provenance: p},
		},
		Relations: []alchemy.Relation{{From: "x1", To: "x2", Type: "USES", Provenance: p}},
		Counts:    alchemy.Counts{Entities: 3, Relations: 1, Deterministic: 4},
	}
}

func TestFindReadsOneLoadOutOfASharedBrain(t *testing.T) {
	l := loaded(t, "run-R")
	got, err := l.Find(context.Background(), "run-R", "superai", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	// The neighbour holds an entity of the same type with the same name. One
	// hit, not two — and the fold is on both sides, so the lowercase needle
	// found "SuperAI".
	if got.Total != 1 || len(got.Nodes) != 1 {
		t.Fatalf("Find = %+v, want exactly the one SuperAI of this load", got)
	}
	if got.Nodes[0].ID != "e1" || got.Nodes[0].Name != "SuperAI" || got.Nodes[0].Type != "System" {
		t.Fatalf("Find returned %+v, want e1/SuperAI/System", got.Nodes[0])
	}
}

func TestFindReportsWhatAPageLeftOut(t *testing.T) {
	l := loaded(t, "run-R")
	got, err := l.Find(context.Background(), "run-R", "", 2)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	// Three entities, two shown. A page that does not say it is a page asks a
	// reader to trust a list that is not the list.
	if got.Total != 3 || len(got.Nodes) != 2 || !got.Truncated() {
		t.Fatalf("Find = %+v, want 2 of 3 and Truncated", got)
	}
	// Ordered by name then id, so the same call cuts the same place twice.
	if got.Nodes[0].Name != "Ada" || got.Nodes[1].Name != "CortexDB" {
		t.Fatalf("page is %v, want the first two by name", []string{got.Nodes[0].Name, got.Nodes[1].Name})
	}
}

func TestTypesCountsOnlyThisLoadsEntities(t *testing.T) {
	l := loaded(t, "run-R")
	got, err := l.Types(context.Background(), "run-R")
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
	}
	// The neighbour's Robot must not be here, and neither must the chunk stubs
	// or the run marker this connector writes under the same run: counting
	// nodes instead of entities reports a vocabulary no ontology declared.
	for _, tc := range got {
		if tc.Type == "Robot" || tc.Type == "" {
			t.Errorf("Types reports %q, which is not an entity type of this load", tc.Type)
		}
	}
}

func TestOfTypeMatchesExactlyAndScopes(t *testing.T) {
	l := loaded(t, "run-R")
	got, err := l.OfType(context.Background(), "run-R", "System", 10)
	if err != nil {
		t.Fatalf("OfType: %v", err)
	}
	if got.Total != 2 {
		t.Fatalf("OfType(System) = %+v, want the 2 of this load", got)
	}
	// A type is declared by an ontology, so this one is compared exactly —
	// the opposite of Find's rule, and deliberately: Find takes what somebody
	// typed, this takes what Types returned.
	lower, err := l.OfType(context.Background(), "run-R", "system", 10)
	if err != nil {
		t.Fatalf("OfType: %v", err)
	}
	if lower.Total != 0 {
		t.Fatalf("OfType(\"system\") matched %d; a fold here would report a vocabulary the load does not have", lower.Total)
	}
}

func TestClaimsCarryTheirOwnProvenanceAndBothDirections(t *testing.T) {
	l := loaded(t, "run-R")
	got, err := l.Claims(context.Background(), "run-R", "e1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	// e1 USES e2 and e3 WORKS_ON e1: both directions in one answer, because an
	// agent asking what is known about a thing does not care which way the
	// extractor wrote the edge.
	if len(got) != 2 {
		t.Fatalf("Claims about e1 = %d, want 2 — %v", len(got), got)
	}
	byType := map[string]recall.Claim{}
	for _, c := range got {
		byType[c.Type] = c
	}
	uses, ok := byType["USES"]
	if !ok {
		t.Fatalf("no USES claim in %v", got)
	}
	if uses.FromID != "e1" || uses.ToID != "e2" || uses.From != "SuperAI" || uses.To != "CortexDB" {
		t.Errorf("USES claim = %+v, want e1/SuperAI -> e2/CortexDB", uses)
	}
	// The edge's own provenance, not its subject's. e1's node came from the
	// llm-extract record and the WORKS_ON edge from the ddl one; a walk that
	// reported the node's would attribute every claim to whatever first
	// mentioned the entity.
	works := byType["WORKS_ON"]
	if !works.Stated || works.Producer != alchemy.ProducerDDL {
		t.Errorf("WORKS_ON = %+v, want the edge's own ddl provenance", works)
	}
	if uses.Stated || uses.Producer != alchemy.ProducerLLMExtract {
		t.Errorf("USES = %+v, want the edge's own llm-extract provenance", uses)
	}
}

func TestDescribeReturnsTheRecordAndNotASentence(t *testing.T) {
	l := loaded(t, "run-R")
	d, err := l.Describe(context.Background(), "run-R", "e1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if d.ID != "e1" || d.Name != "SuperAI" || d.Type != "System" {
		t.Fatalf("Describe = %+v", d)
	}
	// The attributes are the source's own fields, with the types the source
	// gave them. CortexDB's metadata is map[string]string, so `true` was
	// stored as the text "true" and listed under json_attrs; without that list
	// a reader gets the string and compares it against a boolean forever.
	if d.Attributes["public"] != true {
		t.Errorf("Attributes[public] = %#v (%T), want the boolean the source stated", d.Attributes["public"], d.Attributes["public"])
	}
	if d.Attributes["lang"] != "go" {
		t.Errorf("Attributes[lang] = %#v, want the string \"go\"", d.Attributes["lang"])
	}
	// And nothing of alchemy's or CortexDB's own bookkeeping leaked into a map
	// documented as what the source said.
	for _, leaked := range []string{"name", "type", "provenance", "source_document_ids", "_run", "_id", "_source"} {
		if _, bad := d.Attributes[leaked]; bad {
			t.Errorf("Attributes carries %q, which no source stated", leaked)
		}
	}
	// The whole provenance, which is the only place these fields are reachable.
	if d.Provenance.Model != "gemini-3.6-flash-high" || d.Provenance.Confidence != 0.82 ||
		d.Provenance.ReviewedBy != "ada@example.com" || d.Provenance.Ontology != "sds@3" {
		t.Errorf("Provenance = %+v, want the whole of it", d.Provenance)
	}
}

func TestCiteWantsBothHalvesOfTheMarker(t *testing.T) {
	l := loaded(t, "run-R")
	c, err := l.Cite(context.Background(), "run-R", "architecture.pdf", 0)
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if c.Text != "SuperAI uses CortexDB." || c.Start != 100 || c.End != 122 {
		t.Fatalf("Cite = %+v, want the chunk with its offsets", c)
	}
	// A chunk index is unique across a job, so the index alone would resolve.
	// The wrong file with the right number must not hand back the other file's
	// text with nothing about the answer looking wrong.
	if _, err := l.Cite(context.Background(), "run-R", "other.csv", 0); !errors.Is(err, recall.ErrNoCitation) {
		t.Fatalf("Cite with the wrong source = %v, want ErrNoCitation", err)
	}
	// A negative index is not a failure: it means the producer did not work in
	// chunks, which is this store's strongest kind of record.
	if _, err := l.Cite(context.Background(), "run-R", "architecture.pdf", -1); !errors.Is(err, recall.ErrNoChunk) {
		t.Fatalf("Cite(-1) = %v, want ErrNoChunk", err)
	}
}

func TestUnansweredIsReadBackAndFilters(t *testing.T) {
	l := loaded(t, "run-R")
	all, err := l.Unanswered(context.Background(), "run-R", "")
	if err != nil {
		t.Fatalf("Unanswered: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("Unanswered(\"\") = %d, want the fixture's one duplicate", len(all))
	}
	if all[0].Signal != alchemy.DuplicateNameAffix || all[0].Left != "CortexDB" || all[0].Right != "SuperAI" {
		t.Fatalf("Question = %+v", all[0])
	}
	// An empty about is everything, and it is empty rather than a word like
	// "all" because a sentinel that is also a legal search term is a filter
	// that silently stops filtering for one input.
	if lit, err := l.Unanswered(context.Background(), "run-R", "all"); err != nil || len(lit) != 0 {
		t.Fatalf("Unanswered(\"all\") = %v, %v; \"all\" is a search term here, not everything", lit, err)
	}
	if hit, err := l.Unanswered(context.Background(), "run-R", "cortexdb"); err != nil || len(hit) != 1 {
		t.Fatalf("Unanswered(\"cortexdb\") = %v, %v", hit, err)
	}
}

func TestContributionsSeeTheEdgesAsWellAsTheNode(t *testing.T) {
	l := loaded(t, "run-R")
	got, err := l.Contributions(context.Background(), "run-R", "e1")
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	if got.ID != "e1" || got.Type != "System" {
		t.Fatalf("Contributions = %+v", got)
	}
	if len(got.Contributors) == 0 {
		t.Fatal("no contributors; the node's own record is always one")
	}
	// The node's record carries the name; a contribution recovered from an
	// edge does not, and the emptiness is the measurement. Copying the node's
	// name onto every contributor would report that all of them agreed on it.
	named := 0
	for _, c := range got.Contributors {
		if c.Name != "" {
			named++
			if c.Name != "SuperAI" {
				t.Errorf("a contributor calls the node %q", c.Name)
			}
		}
	}
	if named != 1 {
		t.Errorf("%d contributors carry a name, want exactly the node's own record", named)
	}
}

func TestEveryReadRefusesALoadThatIsNotFinished(t *testing.T) {
	l := loaded(t, "run-R")
	ctx := context.Background()

	// Three methods distinguish an absent load, because for them an empty
	// answer and a wrong load name are different mistakes.
	if _, err := l.Describe(ctx, "never-ran", "e1"); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Describe on an unknown load = %v, want ErrNoLoad", err)
	}
	if _, err := l.Contributions(ctx, "never-ran", "e1"); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Contributions on an unknown load = %v, want ErrNoLoad", err)
	}
	if _, err := l.Cite(ctx, "never-ran", "architecture.pdf", 0); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite on an unknown load = %v, want ErrNoLoad", err)
	}
	// The rest answer empty, for the reason the package doc gives: no match is
	// not an error.
	if f, err := l.Find(ctx, "never-ran", "x", 5); err != nil || f.Total != 0 {
		t.Errorf("Find on an unknown load = %+v, %v", f, err)
	}
	if ts, err := l.Types(ctx, "never-ran"); err != nil || len(ts) != 0 {
		t.Errorf("Types on an unknown load = %v, %v", ts, err)
	}
}
