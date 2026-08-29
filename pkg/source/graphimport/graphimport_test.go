package graphimport_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/source/graphimport"
)

// understandAnything is the shape oss-agent's internal/graphimport consumes,
// reduced to the fields this package reads.
const understandAnything = `{
  "name": "ledger-reactor",
  "nodes": [
    {"id": "file:src/main.go", "type": "file", "name": "main.go",
     "filePath": "src/main.go", "summary": "Entry point.", "complexity": "low"},
    {"id": "func:src/main.go:run", "type": "function", "name": "run"}
  ],
  "edges": [
    {"source": "file:src/main.go", "target": "func:src/main.go:run",
     "type": "contains", "direction": "forward", "weight": 1.0}
  ]
}`

func TestParseUnderstandAnythingNodes(t *testing.T) {
	res, err := graphimport.Parse("kg.json", strings.NewReader(understandAnything))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(res.Entities))
	}
	got := res.Entities[0]
	if got.ID != "file:src/main.go" || got.Type != "file" || got.Name != "main.go" {
		t.Errorf("entity[0] = %+v", got)
	}
	want := alchemy.Provenance{Source: "kg.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}
	if got.Provenance != want {
		t.Errorf("provenance = %+v, want %+v", got.Provenance, want)
	}
}

// TestParseEdgeSpellings covers the three endpoint spellings found in real
// documents: Understand-Anything writes source/target, CortexDB's side graph
// writes from/to, and an RDF-shaped export writes subject/object.
func TestParseEdgeSpellings(t *testing.T) {
	const doc = `{
  "nodes": [{"id": "a"}, {"id": "b"}],
  "edges": [
    {"source": "a", "target": "b", "type": "calls"},
    {"from": "a", "to": "b", "relation": "owns"},
    {"subject": "a", "object": "b", "predicate": "cites"},
    {"source": "a", "target": "b", "label": "links"}
  ]
}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 4 {
		t.Fatalf("relations = %d, want 4", len(res.Relations))
	}
	wantTypes := []string{"calls", "owns", "cites", "links"}
	for i, rel := range res.Relations {
		if rel.From != "a" || rel.To != "b" {
			t.Errorf("relation[%d] = %s -> %s, want a -> b", i, rel.From, rel.To)
		}
		if rel.Type != wantTypes[i] {
			t.Errorf("relation[%d].Type = %q, want %q", i, rel.Type, wantTypes[i])
		}
		want := alchemy.Provenance{Source: "kg.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}
		if rel.Provenance != want {
			t.Errorf("relation[%d].Provenance = %+v", i, rel.Provenance)
		}
	}
}

// TestAmbiguousEdgeSpellingIsRefused is the §2.1 rule applied to field names:
// two spellings of the same slot that disagree cannot be resolved from the
// data, so the document is refused rather than read under a coin flip.
func TestAmbiguousEdgeSpellingIsRefused(t *testing.T) {
	const doc = `{
  "nodes": [{"id": "a"}, {"id": "b"}, {"id": "c"}],
  "edges": [{"from": "a", "source": "c", "target": "b", "type": "calls"}]
}`
	_, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err == nil {
		t.Fatal("Parse: want an error for an edge stating both from and source")
	}
	var amb *graphimport.AmbiguityError
	if !errors.As(err, &amb) {
		t.Fatalf("error %v (%T), want *graphimport.AmbiguityError", err, err)
	}
	for _, want := range []string{"from", "source", `"a"`, `"c"`, "edge 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// TestAgreeingSpellingsAreNotAmbiguous: a document that writes both spellings
// with the same value has stated one thing twice, which is redundant and not
// a question anyone has to answer.
func TestAgreeingSpellingsAreNotAmbiguous(t *testing.T) {
	const doc = `{
  "nodes": [{"id": "a"}, {"id": "b"}],
  "edges": [{"from": "a", "source": "a", "target": "b", "to": "b", "type": "calls"}]
}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 1 || res.Relations[0].From != "a" || res.Relations[0].To != "b" {
		t.Fatalf("relations = %+v", res.Relations)
	}
}

// TestNodeWithoutIDIsAGuess: CortexDB side graphs write nodes as {name, type,
// note} with no id at all, and their edges name nodes by that name. Falling
// back to the name is the only reading that resolves those edges, but it is
// an inference about identity, so it is reported (§2.1).
func TestNodeWithoutIDIsAGuess(t *testing.T) {
	const doc = `{
  "nodes": [{"name": "step-1", "type": "step"}, {"id": "step-2", "name": "second"}],
  "edges": [{"from": "step-1", "to": "step-2", "type": "next"}]
}`
	res, err := graphimport.Parse("side.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Entities[0].ID != "step-1" {
		t.Errorf("entity[0].ID = %q, want %q", res.Entities[0].ID, "step-1")
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none: the edge resolves", res.Violations)
	}
	if len(res.Guesses) != 1 {
		t.Fatalf("guesses = %d, want 1 (only the id-less node is a guess)", len(res.Guesses))
	}
	g := res.Guesses[0]
	if g.ChosenAs != "step-1" || g.Field != "node 0 id" {
		t.Errorf("guess = %+v", g)
	}
	if g.Reason == "" {
		t.Error("guess has no Reason; a guess nobody can check is not reported")
	}
	want := alchemy.Provenance{Source: "side.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}
	if g.Provenance != want {
		t.Errorf("guess provenance = %+v, want %+v", g.Provenance, want)
	}
}

// TestDanglingRelationIsReportedNotDropped: §7.3 — a violation is
// attributable and excludable, and the rest of the graph is usable without
// it. Dropping the edge instead would make the surviving graph look complete.
func TestDanglingRelationIsReportedNotDropped(t *testing.T) {
	const doc = `{
  "nodes": [{"id": "a"}],
  "edges": [{"source": "a", "target": "ghost", "type": "calls"}]
}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 1 {
		t.Fatalf("relations = %d, want 1: a dangling edge is reported, never dropped", len(res.Relations))
	}
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %+v, want 1", res.Violations)
	}
	v := res.Violations[0]
	if v.Kind != alchemy.ViolationDanglingRelation {
		t.Errorf("kind = %q, want %q", v.Kind, alchemy.ViolationDanglingRelation)
	}
	if !strings.Contains(v.Detail, "ghost") {
		t.Errorf("detail %q does not name the missing node", v.Detail)
	}
	if v.Subject != "a -[calls]-> ghost" {
		t.Errorf("subject = %q", v.Subject)
	}
	want := alchemy.Provenance{Source: "kg.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}
	if v.Provenance != want {
		t.Errorf("violation provenance = %+v", v.Provenance)
	}
}

// TestDanglingBothEndsIsOneViolation: one broken edge is one thing a person
// has to fix, so it is one entry in the queue naming both missing nodes.
func TestDanglingBothEndsIsOneViolation(t *testing.T) {
	const doc = `{"nodes": [{"id": "a"}], "edges": [{"from": "x", "to": "y", "type": "calls"}]}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(res.Violations))
	}
	d := res.Violations[0].Detail
	if !strings.Contains(d, "x") || !strings.Contains(d, "y") {
		t.Errorf("detail %q does not name both missing nodes", d)
	}
}

// TestSummariesBecomeChunks: node summaries are what the embedder stage
// vectorises. A node with no summary produces no chunk, which is normal.
func TestSummariesBecomeChunks(t *testing.T) {
	const doc = `{
  "nodes": [
    {"id": "a", "name": "main.go", "summary": "Entry point."},
    {"id": "b", "name": "run", "description": "Runs the thing."},
    {"id": "c", "name": "quiet"},
    {"id": "d", "name": "blank", "summary": "   "}
  ]
}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Chunks) != 2 {
		t.Fatalf("chunks = %+v, want 2 (nodes c and d state no summary)", res.Chunks)
	}
	for i, c := range res.Chunks {
		if c.Index != i {
			t.Errorf("chunk[%d].Index = %d", i, c.Index)
		}
		if c.Source != "kg.json" {
			t.Errorf("chunk[%d].Source = %q", i, c.Source)
		}
		if c.Strategy != graphimport.ChunkStrategy {
			t.Errorf("chunk[%d].Strategy = %q, want %q", i, c.Strategy, graphimport.ChunkStrategy)
		}
		// A node summary is not a span of the document's text, so there is no
		// honest byte range to give. -1 is the contract's existing "not
		// applicable", the same value Provenance.Chunk carries here.
		if c.Start != -1 || c.End != -1 {
			t.Errorf("chunk[%d] span = [%d,%d], want [-1,-1]", i, c.Start, c.End)
		}
	}
	if res.Chunks[0].Text != "Entry point." || res.Chunks[0].Heading != "main.go" {
		t.Errorf("chunk[0] = %+v", res.Chunks[0])
	}
	if res.Chunks[1].Text != "Runs the thing." || res.Chunks[1].Heading != "run" {
		t.Errorf("chunk[1] = %+v", res.Chunks[1])
	}
}

// TestExtraMembersBecomeAttributes: Entity.Attributes is "whatever the source
// stated beyond type and name", and Understand-Anything states a good deal —
// filePath, tags, complexity — that a reader of the graph will want.
func TestExtraMembersBecomeAttributes(t *testing.T) {
	res, err := graphimport.Parse("kg.json", strings.NewReader(understandAnything))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	attrs := res.Entities[0].Attributes
	if attrs["filePath"] != "src/main.go" || attrs["complexity"] != "low" {
		t.Errorf("attributes = %#v", attrs)
	}
	for _, consumed := range []string{"id", "type", "name", "summary"} {
		if _, ok := attrs[consumed]; ok {
			t.Errorf("attributes carry %q, which was already read into a field", consumed)
		}
	}
	if res.Entities[1].Attributes != nil {
		t.Errorf("a node stating nothing extra gets no attribute map, got %#v", res.Entities[1].Attributes)
	}
	edge := res.Relations[0].Attributes
	if edge["direction"] != "forward" || edge["weight"] != 1.0 {
		t.Errorf("edge attributes = %#v", edge)
	}
}

// TestDuplicateNodeIDIsRefused: two nodes with one id are one document
// contradicting itself, and unlike a dangling edge the damage is not local —
// every edge naming that id silently attaches to whichever copy won.
func TestDuplicateNodeIDIsRefused(t *testing.T) {
	const doc = `{"nodes": [
	  {"id": "a", "type": "file", "name": "one"},
	  {"id": "a", "type": "file", "name": "two"}
	]}`
	_, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err == nil {
		t.Fatal("Parse: want an error for a repeated node id")
	}
	var dup *graphimport.DuplicateNodeError
	if !errors.As(err, &dup) {
		t.Fatalf("error %v (%T), want *graphimport.DuplicateNodeError", err, err)
	}
	if dup.ID != "a" || dup.First != 0 || dup.Second != 1 {
		t.Errorf("dup = %+v", dup)
	}
	if !strings.Contains(err.Error(), "a") {
		t.Errorf("error %q does not name the id", err.Error())
	}
}

// TestIdenticalRepeatedNodeIsNotCorruption: a node repeated with every field
// the same asserts one thing twice. There is nothing to decide, so there is
// nothing to refuse; it is folded back into one entity.
func TestIdenticalRepeatedNodeIsNotCorruption(t *testing.T) {
	const doc = `{"nodes": [
	  {"id": "a", "type": "file", "name": "one", "summary": "S", "tags": ["x"]},
	  {"id": "a", "type": "file", "name": "one", "summary": "S", "tags": ["x"]}
	]}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 1 {
		t.Errorf("entities = %d, want 1", len(res.Entities))
	}
	if len(res.Chunks) != 1 {
		t.Errorf("chunks = %d, want 1: one node, one summary", len(res.Chunks))
	}
}

func TestMalformedJSON(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"garbage", "not json at all"},
		{"truncated", `{"nodes": [{"id": "a"},`},
		{"empty", ""},
		{"wrong type for nodes", `{"nodes": "a string"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := graphimport.Parse("kg.json", strings.NewReader(tc.in))
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", tc.in, res)
			}
			// The offset is the whole point: a graph document is machine
			// written and large, and "invalid character" with no position is
			// a bug report nobody can act on.
			if !strings.Contains(err.Error(), "offset") {
				t.Errorf("error %q names no offset", err.Error())
			}
			if !strings.Contains(err.Error(), "kg.json") {
				t.Errorf("error %q does not name the source", err.Error())
			}
		})
	}
}

// A reader that fails part way through is not a malformed document and must
// not be reported as one.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk went away") }

func TestReadErrorIsNotAParseError(t *testing.T) {
	_, err := graphimport.Parse("kg.json", failingReader{})
	if err == nil || !strings.Contains(err.Error(), "disk went away") {
		t.Fatalf("err = %v, want the reader's own error", err)
	}
}

// TestContainerSpellings: the two collections are spelled differently too —
// a document that calls its lists "entities" and "relations" is describing
// the same graph as one that calls them "nodes" and "edges".
func TestContainerSpellings(t *testing.T) {
	const doc = `{
  "entities": [{"id": "a"}, {"id": "b"}],
  "relations": [{"from": "a", "to": "b", "type": "calls"}]
}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 2 || len(res.Relations) != 1 {
		t.Fatalf("entities = %d, relations = %d; want 2 and 1", len(res.Entities), len(res.Relations))
	}
}

// A document carrying two non-empty spellings of the same collection is the
// same undecidable question as an edge with two endpoints, at document scale.
func TestAmbiguousContainerIsRefused(t *testing.T) {
	const doc = `{"nodes": [{"id": "a"}], "entities": [{"id": "b"}], "edges": []}`
	_, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	var amb *graphimport.AmbiguityError
	if !errors.As(err, &amb) {
		t.Fatalf("error %v (%T), want *graphimport.AmbiguityError", err, err)
	}
	if amb.Slot != "nodes" {
		t.Errorf("slot = %q, want %q", amb.Slot, "nodes")
	}
}

// An empty list is not a competing claim: writing "edges": [] alongside a
// populated "relations" states no relations twice over, and refusing that
// would reject documents whose writer emits every key it knows.
func TestEmptyContainerIsNotAmbiguous(t *testing.T) {
	const doc = `{"nodes": [{"id": "a"}, {"id": "b"}], "entities": [],
	              "edges": [], "relations": [{"from": "a", "to": "b", "type": "calls"}]}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 2 || len(res.Relations) != 1 {
		t.Fatalf("entities = %d, relations = %d", len(res.Entities), len(res.Relations))
	}
}

// TestCounts is §5's obligation: a returned graph comes with the numbers
// needed to distrust it. For this source they are dull on purpose — nothing
// was inferred — and dull numbers still have to be stated, because a reader
// comparing this run against an llm-extract run needs both columns filled in.
func TestCounts(t *testing.T) {
	const doc = `{
  "nodes": [{"id": "a", "summary": "S"}, {"name": "b"}],
  "edges": [{"from": "a", "to": "b", "type": "calls"},
            {"from": "a", "to": "ghost", "type": "calls"}]
}`
	res, err := graphimport.Parse("kg.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := res.Counts()
	want := alchemy.Counts{
		Entities: 2, Relations: 2,
		Deterministic: 2, Inferred: 0,
		Violations: 1, Guesses: 1,
	}
	if got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}

// TestIncompleteEdgeIsRefused: an edge that names no endpoint is not a
// dangling edge — a dangling edge points at something, just something the
// document forgot to include, and it is reported and kept. An edge with no
// source states nothing at all, and keeping it would put a relation with an
// empty From into the graph.
func TestIncompleteEdgeIsRefused(t *testing.T) {
	cases := map[string]string{
		"no source": `{"nodes": [{"id": "a"}], "edges": [{"target": "a", "type": "calls"}]}`,
		"no target": `{"nodes": [{"id": "a"}], "edges": [{"source": "a", "type": "calls"}]}`,
		"no type":   `{"nodes": [{"id": "a"}], "edges": [{"source": "a", "target": "a"}]}`,
		"blank":     `{"nodes": [{"id": "a"}], "edges": [{"source": "  ", "target": "a", "type": "x"}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := graphimport.Parse("kg.json", strings.NewReader(doc))
			if err == nil {
				t.Fatal("Parse: want an error")
			}
			if !strings.Contains(err.Error(), "edge 0") {
				t.Errorf("error %q does not locate the edge", err.Error())
			}
		})
	}
}

// The table in DESIGN.md §2 calls this source's determinism High. Nothing
// here reads a map in iteration order into the output, and the same bytes
// must give the same graph every time.
func TestParseIsDeterministic(t *testing.T) {
	first, err := graphimport.Parse("kg.json", strings.NewReader(understandAnything))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := graphimport.Parse("kg.json", strings.NewReader(understandAnything))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first, again)
		}
	}
}

// TestUnrecognisedDocumentIsRefused: handing this package a JSON document
// that is not a graph — CortexDB's knowledge_graph_export writes
// {"format": "turtle", "content": "..."} — must not come back as a graph with
// nothing in it. An empty result is indistinguishable from a graph that was
// genuinely empty, and that is the quiet failure §2.1 is about.
func TestUnrecognisedDocumentIsRefused(t *testing.T) {
	const doc = `{"format": "turtle", "content": "@prefix ex: <http://e/> ."}`
	_, err := graphimport.Parse("export.json", strings.NewReader(doc))
	if err == nil {
		t.Fatal("Parse: want an error for a document with no node or edge list")
	}
	for _, want := range []string{"nodes", "edges", "export.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// A document that says it has no nodes is a different statement from one that
// never mentions nodes, and it is a legitimate one.
func TestExplicitlyEmptyGraphIsAccepted(t *testing.T) {
	res, err := graphimport.Parse("kg.json", strings.NewReader(`{"nodes": [], "edges": []}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 0 || len(res.Relations) != 0 {
		t.Errorf("res = %+v, want empty", res)
	}
	if got := res.Counts(); got != (alchemy.Counts{}) {
		t.Errorf("counts = %+v, want zero", got)
	}
}

// TestCortexDBGraphflowShape is the second real format: nodes as
// id/label/type/summary, edges as source/target/relation with a confidence
// the upstream extractor asserted. That confidence is the upstream model's,
// not ours — Provenance.Confidence stays 0, because what this package did was
// read a file (§5b).
func TestCortexDBGraphflowShape(t *testing.T) {
	const doc = `{
  "nodes": [
    {"id": "n1", "label": "CortexDB", "type": "System", "summary": "A local-first store.",
     "source_file": "arch.md", "metadata": {"team": "core"}},
    {"id": "n2", "label": "SQLite", "type": "System"}
  ],
  "edges": [{"source": "n1", "target": "n2", "relation": "USES", "confidence": 0.82, "directed": true}]
}`
	res, err := graphimport.Parse("graph.json", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Entities[0].Name != "CortexDB" || res.Entities[0].Type != "System" {
		t.Errorf("entity[0] = %+v", res.Entities[0])
	}
	if res.Entities[0].Attributes["source_file"] != "arch.md" {
		t.Errorf("attributes = %#v", res.Entities[0].Attributes)
	}
	rel := res.Relations[0]
	if rel.Type != "USES" {
		t.Errorf("relation type = %q", rel.Type)
	}
	if rel.Provenance.Confidence != 0 || rel.Provenance.Model != "" {
		t.Errorf("provenance = %+v: a graph import infers nothing and has no model", rel.Provenance)
	}
	if rel.Attributes["confidence"] != 0.82 {
		t.Errorf("the upstream confidence belongs in attributes, got %#v", rel.Attributes)
	}
	if res.Chunks[0].Text != "A local-first store." || len(res.Chunks) != 1 {
		t.Errorf("chunks = %+v", res.Chunks)
	}
}
