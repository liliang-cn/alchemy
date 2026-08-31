package rdf

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// loaded writes the fixture and returns the loader and the load's name.
func loaded(t *testing.T, o Options) (*Loader, string) {
	t.Helper()
	l := liveLoader(t, o)
	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return l, l.opts.RunID
}

func TestAnAnchorSearchFindsEntitiesByPartOfTheirName(t *testing.T) {
	l, load := loaded(t, Options{})
	found, err := l.Find(context.Background(), load, "super", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found.Nodes) != 1 || found.Nodes[0].ID != "e1" {
		t.Fatalf("Find(\"super\") = %+v, want the one entity called SuperAI", found.Nodes)
	}
	if found.Nodes[0].Type != "System" || found.Nodes[0].Name != "SuperAI" {
		t.Errorf("the anchor lost its type or its name: %+v", found.Nodes[0])
	}
	if found.Total != 1 || found.Truncated() {
		t.Errorf("Total = %d, Truncated = %v, want 1 and false", found.Total, found.Truncated())
	}
}

// A page that does not say it is a page asks a reader to trust a list that is
// not the list. Measured, one connector over: an anchor search matched fourteen
// and returned twelve, and in seven runs out of eight the agent handed the
// truncated page invented an id rather than reporting the truncation.
func TestAnAnchorSearchSaysHowManyMatchedAndNotHowManyCameBack(t *testing.T) {
	l, load := loaded(t, Options{})
	// Two of the fixture's three entities are called something containing "a" —
	// Ada and SuperAI — and CortexDB is not, so a page of one leaves exactly one
	// match unshown and the count has something to be right about.
	found, err := l.Find(context.Background(), load, "a", 1)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found.Nodes) != 1 {
		t.Fatalf("Find returned %d nodes for a limit of 1", len(found.Nodes))
	}
	if found.Total != 2 {
		t.Errorf("Total = %d, want 2: the count must be how many matched", found.Total)
	}
	if !found.Truncated() {
		t.Error("Truncated = false on a page that left a match unshown")
	}
	// Ordered by name then id, so the same limit cuts the same place twice.
	if found.Nodes[0].Name != "Ada" {
		t.Errorf("the first page holds %q, want the first name in order", found.Nodes[0].Name)
	}
}

// The bookkeeping this connector writes must never reach an agent as a claim.
// In RDF that is a sharper requirement than elsewhere, because the annotation
// triples are themselves triples: an unrestricted walk over this graph returns
// the edge and every one of its provenance statements.
func TestAWalkReturnsTheExtractedEdgesAndNothingElseTheConnectorWrote(t *testing.T) {
	l, load := loaded(t, Options{})
	claims, err := l.Claims(context.Background(), load, "e1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	// SuperAI uses CortexDB, and Ada works on SuperAI. Both directions, and
	// nothing else — not the rdf:type statement, not the rdfs:label, not the
	// skos:closeMatch the duplicate finding wrote between e1 and e2, and not any
	// of the al: predicates.
	if len(claims) != 2 {
		t.Fatalf("Claims about e1 returned %d rows, want 2:\n%v", len(claims), render(claims))
	}
	want := map[string]bool{
		"SuperAI -[USES]-> CortexDB": true,
		"Ada -[WORKS_ON]-> SuperAI":  true,
	}
	for _, c := range claims {
		key := c.From + " -[" + c.Type + "]-> " + c.To
		if !want[key] {
			t.Errorf("a walk returned %q, which is not an extracted edge", key)
		}
		delete(want, key)
	}
	for k := range want {
		t.Errorf("a walk did not return %q", k)
	}
}

// Each claim carries its own provenance and not its subject's. The fixture is
// built so a mistake is visible: the USES edge was proposed by a model from
// chunk 14 of a PDF, and the WORKS_ON edge by a DDL reader from a .sql file
// with no chunk at all.
func TestEachClaimCarriesTheProvenanceOfItsOwnAssertion(t *testing.T) {
	l, load := loaded(t, Options{})
	claims, err := l.Claims(context.Background(), load, "e1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	for _, c := range claims {
		switch c.Type {
		case "USES":
			if c.Producer != alchemy.ProducerLLMExtract || c.Source != "architecture.pdf" || c.Chunk != 14 {
				t.Errorf("USES came back as %s, want llm-extract [architecture.pdf#14]", c)
			}
			if c.Stated {
				t.Errorf("USES came back stated; a model proposing an edge is inferred")
			}
		case "WORKS_ON":
			if c.Producer != alchemy.ProducerDDL || c.Source != "schema.sql" || c.Chunk != -1 {
				t.Errorf("WORKS_ON came back as %s, want ddl [schema.sql]", c)
			}
			if !c.Stated {
				t.Errorf("WORKS_ON came back inferred; a foreign key states the edge")
			}
		}
	}
}

func TestACitationResolvesToTheTextAndItsPlaceInTheFile(t *testing.T) {
	l, load := loaded(t, Options{})
	cit, err := l.Cite(context.Background(), load, "architecture.pdf", 14)
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	want := recall.Citation{
		Source: "architecture.pdf", Index: 14, Start: 100, End: 122, Text: "SuperAI uses CortexDB.",
	}
	if cit != want {
		t.Errorf("Cite = %+v, want %+v", cit, want)
	}
}

// TestACitationAskedForUnderTheWrongLoadReturnsNoCitationAndNotAnotherLoadsText
// is the failure pkg/recall exists for, reproduced and refused.
//
// Its package doc describes it: one company profile PDF was present under an old
// import with no byte offsets and under the current one, a citation lookup
// written without a load filter resolved against the wrong import, and nothing
// about the answer looked wrong. Two loads here hold the same source and the
// same chunk index with different text, which is exactly that corpus.
func TestACitationAskedForUnderTheWrongLoadReturnsNoCitationAndNotAnotherLoadsText(t *testing.T) {
	ctx := context.Background()
	l := liveLoader(t, Options{})

	old := fixture()
	old.Chunks[0].Text = "the old import, with no byte offsets"
	old.Chunks[0].Start, old.Chunks[0].End = 0, 0
	old.Counts = old.Derivable()
	if _, err := sink.Load(ctx, l, old, sink.Options{Load: "ld-old"}); err != nil {
		t.Fatalf("loading the old import: %v", err)
	}

	current := fixture()
	current.Entities[0].Name = "SuperAI, renamed"
	current.Counts = current.Derivable()
	if _, err := sink.Load(ctx, l, current, sink.Options{Load: "ld-current"}); err != nil {
		t.Fatalf("loading the current import: %v", err)
	}

	// Both loads hold architecture.pdf#14 and they hold different text.
	fromOld, err := l.Cite(ctx, "ld-old", "architecture.pdf", 14)
	if err != nil {
		t.Fatalf("Cite in the old load: %v", err)
	}
	if fromOld.Text != "the old import, with no byte offsets" {
		t.Fatalf("the old load's citation returned %q", fromOld.Text)
	}
	fromCurrent, err := l.Cite(ctx, "ld-current", "architecture.pdf", 14)
	if err != nil {
		t.Fatalf("Cite in the current load: %v", err)
	}
	if fromCurrent.Text == fromOld.Text {
		t.Fatal("the two loads returned the same text, so the load filter is not doing anything")
	}

	// And a load that holds no such chunk says so, rather than answering with
	// one that does.
	only := fixture()
	only.Chunks = nil
	only.Counts = only.Derivable()
	if _, err := sink.Load(ctx, l, only, sink.Options{Load: "ld-nochunks"}); err != nil {
		t.Fatalf("loading a result with no chunks: %v", err)
	}
	_, err = l.Cite(ctx, "ld-nochunks", "architecture.pdf", 14)
	if !errors.Is(err, recall.ErrNoCitation) {
		t.Fatalf("Cite in a load with no chunks: err = %v, want recall.ErrNoCitation", err)
	}
}

// A load that is not in the store is a different mistake with a different fix:
// the caller named the wrong import, which is the bug the load parameter exists
// for arriving as a typo instead of as a wrong answer.
func TestACitationInALoadThatIsNotHereSaysSoRatherThanSayingNoCitation(t *testing.T) {
	l, _ := loaded(t, Options{})
	_, err := l.Cite(context.Background(), "ld-never-imported", "architecture.pdf", 14)
	if !errors.Is(err, recall.ErrNoLoad) {
		t.Fatalf("Cite in an absent load: err = %v, want recall.ErrNoLoad", err)
	}
}

// A half-written load must answer nothing at all. Here the marker is written
// false by Begin and flipped only by Commit, so a load that was aborted is one
// every read declines to serve — including Cite, which reports it as an absent
// load because a load that is still arriving is not one anything may be cited
// from.
func TestALoadThatNeverFinishedAnswersNothing(t *testing.T) {
	ctx := context.Background()
	l := liveLoader(t, Options{})
	tx, err := l.Begin(ctx, sink.Ident{Load: "ld-halfway", Digest: "d-halfway"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	res := fixture()
	if err := tx.Entities(ctx, res.Entities); err != nil {
		t.Fatalf("Entities: %v", err)
	}
	if err := tx.Chunks(ctx, []sink.Chunk{{Chunk: res.Chunks[0]}}); err != nil {
		t.Fatalf("Chunks: %v", err)
	}
	if err := tx.Abort(ctx); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	found, err := l.Find(ctx, "ld-halfway", "super", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found.Nodes) != 0 {
		t.Errorf("an unfinished load answered an anchor search with %+v", found.Nodes)
	}
	if _, err := l.Cite(ctx, "ld-halfway", "architecture.pdf", 14); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite in an unfinished load: err = %v, want recall.ErrNoLoad", err)
	}
	// The entities really are there — this is a load that is unfinished rather
	// than a load that is empty, which is what makes the refusal meaningful.
	rows := l.ask(t, "SELECT (COUNT(*) AS ?n) WHERE { GRAPH <"+l.loadIRI("ld-halfway")+"> { ?s <"+pID+"> ?id } }")
	if len(rows) == 0 || rows[0]["n"].Value == "0" {
		t.Fatal("nothing was written, so this test proves nothing about unfinished loads")
	}
}

func TestTheIdentityQuestionsCanBeAskedAndAreNotClaims(t *testing.T) {
	l, load := loaded(t, Options{})
	qs, err := l.Unanswered(context.Background(), load, "")
	if err != nil {
		t.Fatalf("Unanswered: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("Unanswered returned %d questions, want the one the fixture carries: %+v", len(qs), qs)
	}
	q := qs[0]
	if q.Signal != alchemy.DuplicateNameAffix || q.Left != "CortexDB" || q.Right != "SuperAI" {
		t.Errorf("the question came back as %+v", q)
	}
	// Searching by either side's name finds it, because a person asks about a
	// thing by its name and alchemy keeps the pair in four fields.
	for _, about := range []string{"cortexdb", "superai", "word added"} {
		got, err := l.Unanswered(context.Background(), load, about)
		if err != nil {
			t.Fatalf("Unanswered(%q): %v", about, err)
		}
		if len(got) != 1 {
			t.Errorf("Unanswered(%q) returned %d, want 1", about, len(got))
		}
	}
	if got, err := l.Unanswered(context.Background(), load, "nothing-matches-this"); err != nil || len(got) != 0 {
		t.Errorf("Unanswered on a term nothing matches = %v, %v", got, err)
	}
}

// The duplicate is in the graph as skos:closeMatch and never as owl:sameAs. A
// reasoner given sameAs is entitled to merge the two nodes and rewrite every
// triple about either onto both — answering, on alchemy's behalf, a question
// alchemy explicitly refuses to answer.
func TestADuplicateIsACloseMatchAndNeverASameAs(t *testing.T) {
	l, load := loaded(t, Options{})
	g := l.loadIRI(load)
	if rows := l.ask(t, "SELECT ?a ?b WHERE { GRAPH <"+g+"> { ?a <"+skosCloseMatch+"> ?b } }"); len(rows) != 1 {
		t.Errorf("the duplicate finding wrote %d skos:closeMatch statements, want 1", len(rows))
	}
	rows := l.ask(t, "SELECT ?a ?b WHERE { GRAPH <"+g+"> { ?a <http://www.w3.org/2002/07/owl#sameAs> ?b } }")
	if len(rows) != 0 {
		t.Fatalf("this load asserts owl:sameAs %d times; a duplicate is a question nobody has "+
			"answered and sameAs is an assertion of identity that a reasoner will act on", len(rows))
	}
}

func render(claims []recall.Claim) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.String())
	}
	return out
}
