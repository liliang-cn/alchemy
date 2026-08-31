// Package sinkconform is the suite every connector's sink.Sink passes.
//
// DESIGN.md §9 already argues for this shape one level down — "a conformance
// suite the in-memory stores pass as evidence the second implementation is
// faithful" — and an interface extracted from four existing implementations
// needs it more, not less. The four were written apart and agreed about the
// envelope and disagreed about everything under it; a suite that only the
// connector's own author runs would let the agreement decay back into four
// answers without anybody noticing.
//
// It tests only what §4.1 puts above the line. Nothing here knows how a store
// checks for an existing load, what it batches, what it indexes, or what it
// reserves — a test that did would be the interface reaching below the line
// through its test suite, which is how a boundary is lost.
package sinkconform

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Open makes a store for one test. It is a function rather than a value
// because every connector's fixture is per-test — a private schema, a private
// collection, a private label — and sharing one across the cases below would
// make each case depend on the ones before it.
type Open func(t *testing.T) sink.Sink

// Run executes the whole suite against one store.
func Run(t *testing.T, open Open) {
	t.Helper()
	t.Run("a_clean_result_loads_and_reports_what_it_wrote", func(t *testing.T) {
		rep, err := sink.Load(context.Background(), open(t), Graph(4), sink.Options{Load: "load-1"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if rep.Load != "load-1" || rep.Digest == "" {
			t.Fatalf("report = %+v, want the name and the digest it was loaded under", rep)
		}
		if rep.Converged {
			t.Error("Converged = true on a first load")
		}
	})

	t.Run("the_same_result_twice_is_a_no_op", func(t *testing.T) {
		s := open(t)
		if _, err := sink.Load(context.Background(), s, Graph(4), sink.Options{Load: "load-1"}); err != nil {
			t.Fatalf("first Load: %v", err)
		}
		rep, err := sink.Load(context.Background(), s, Graph(4), sink.Options{Load: "load-1"})
		if err != nil {
			t.Fatalf("second Load: %v", err)
		}
		if !rep.Converged {
			t.Fatal("Converged = false; a retried nightly import must cost nothing")
		}
	})

	t.Run("a_different_result_under_one_name_is_refused", func(t *testing.T) {
		s := open(t)
		if _, err := sink.Load(context.Background(), s, Graph(4), sink.Options{Load: "load-1"}); err != nil {
			t.Fatalf("first Load: %v", err)
		}
		other := Graph(4)
		other.Entities[0].Name = "SuperAI, renamed"
		other.Counts = other.Derivable()
		_, err := sink.Load(context.Background(), s, other, sink.Options{Load: "load-1"})
		if !errors.Is(err, sink.ErrExists) {
			t.Fatalf("err = %v, want sink.ErrExists: two different graphs under one name is a question nothing in the data answers", err)
		}
	})

	t.Run("replace_is_how_a_caller_means_it", func(t *testing.T) {
		s := open(t)
		if _, err := sink.Load(context.Background(), s, Graph(4), sink.Options{Load: "load-1"}); err != nil {
			t.Fatalf("first Load: %v", err)
		}
		other := Graph(4)
		other.Entities[0].Name = "SuperAI, renamed"
		other.Counts = other.Derivable()
		rep, err := sink.Load(context.Background(), s, other, sink.Options{Load: "load-1", Replace: true})
		if err != nil {
			t.Fatalf("Load with Replace: %v", err)
		}
		if rep.Converged {
			t.Error("Converged = true, but this is a different graph")
		}
	})

	t.Run("a_result_that_retires_a_record_it_does_not_contain_loads", func(t *testing.T) {
		// Graph carries one supersession naming a record that is not in it, so
		// this is the whole suite's fixture asserting the rule rather than a
		// case of its own. What is checked here is that the store wrote the
		// load anyway: a connector that treated an unresolvable Retires as a
		// broken reference would refuse every correction to a corpus loaded
		// before it, which is every correction there is.
		rep, err := sink.Load(context.Background(), open(t), Graph(4), sink.Options{Load: "load-1"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if rep.Supersessions != 1 {
			t.Fatalf("Report.Supersessions = %d, want the one this result states", rep.Supersessions)
		}
	})

	t.Run("a_result_that_retires_something_else_is_a_different_import", func(t *testing.T) {
		// The graph is identical in both loads and only the retirement differs.
		// A store whose content address were blind to it would converge the
		// second onto the first, write nothing, and go on holding the record
		// somebody had just said was over -- so this is the one case in the
		// suite where "refused" is the passing answer and silence is the bug.
		s := open(t)
		if _, err := sink.Load(context.Background(), s, Graph(4), sink.Options{Load: "load-1"}); err != nil {
			t.Fatalf("first Load: %v", err)
		}
		other := Graph(4)
		other.Supersessions[0].Retires = "e-cto-somebody-else"
		_, err := sink.Load(context.Background(), s, other, sink.Options{Load: "load-1"})
		if !errors.Is(err, sink.ErrExists) {
			t.Fatalf("err = %v, want sink.ErrExists: a result that retires another record is another import", err)
		}
	})

	// The case this whole product exists to produce, and the one that could not
	// be loaded into two of the five stores.
	//
	// pkg/preflight legalised two records under one ID that agree about what the
	// node is -- corroboration, the ordinary shape of a corpus merged from more
	// than one document -- and the connectors were written before it did. Two of
	// them still refused the whole result, so a graph a second source agreed
	// with was a graph they would not hold. It is in the shared suite rather
	// than in either of their own tests because all five have to answer it the
	// same way, and the reason two of them had the old answer is that nothing
	// asked all five at once.
	t.Run("a_node_two_sources_agree_about_loads_as_one", func(t *testing.T) {
		res := Corroborated(0)
		rep, err := sink.Load(context.Background(), open(t), res, sink.Options{Load: "load-corroborated"})
		if err != nil {
			t.Fatalf("Load of a graph two sources agree about: %v", err)
		}
		// One row, not two: every one of the five keys entities by ID.
		if rep.Entities != 2 {
			t.Errorf("Entities = %d, want 2; three records under two IDs is two rows", rep.Entities)
		}
		// And the fold is reported rather than silent, because what it costs --
		// the second record's provenance -- is not recoverable from the row.
		if rep.Corroborated != 1 {
			t.Errorf("Corroborated = %d, want 1; a store that folds a record without saying so "+
				"reports a merged graph as a single-source one", rep.Corroborated)
		}
	})

	// The other half of the same rule, so that widening it did not widen it into
	// nothing. Two records under one ID that describe DIFFERENT nodes are still
	// a broken result: relations name entities by ID, and either winner leaves
	// some edge pointing at the wrong node.
	t.Run("a_node_two_sources_disagree_about_is_still_refused", func(t *testing.T) {
		res := Corroborated(0)
		res.Entities[2].Name = "Something Else Entirely"
		res.Counts = res.Derivable()
		if _, err := sink.Load(context.Background(), open(t), res, sink.Options{Load: "load-collided"}); err == nil {
			t.Fatal("Load of two different nodes under one ID succeeded; " +
				"corroboration is agreement, and this is a collision")
		}
	})

	t.Run("a_result_with_no_vectors_loads", func(t *testing.T) {
		res := Graph(0)
		if _, err := sink.Load(context.Background(), open(t), res, sink.Options{Load: "load-1"}); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})

	t.Run("a_batch_size_of_one_loads_the_same_graph", func(t *testing.T) {
		s := open(t)
		one, err := sink.Load(context.Background(), s, Graph(4), sink.Options{Load: "load-1", Batch: 1})
		if err != nil {
			t.Fatalf("Load at Batch 1: %v", err)
		}
		// The digest is a property of the result, not of how it was carried.
		// §8.4's whole point is that a graph too big for one message is the
		// same graph, and a store whose idea of what it holds depended on the
		// batch size would refuse every large re-load.
		if one.Digest != sink.Digest(Graph(4)) {
			t.Fatalf("digest = %q, want the result's own", one.Digest)
		}
		again, err := sink.Load(context.Background(), s, Graph(4), sink.Options{Load: "load-1"})
		if err != nil {
			t.Fatalf("Load again: %v", err)
		}
		if !again.Converged {
			t.Fatal("a graph loaded one record at a time is not recognised as the same graph loaded whole")
		}
	})
}

// Graph is the suite's corpus: entities, an edge, chunks with embeddings of
// width dim, and one finding of each kind that a store might file separately.
// dim of 0 is a result with no vectors at all, which is every schema import.
func Graph(dim int) alchemy.Result {
	prov := func(c int) alchemy.Provenance {
		return alchemy.Provenance{
			Source: "architecture.md", Chunk: c, Producer: alchemy.ProducerLLMExtract,
			Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading", Confidence: 0.82,
			RuleSet: "rs-9f21", RuledBy: "authored/violation/type=Flag",
		}
	}
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI", Attributes: map[string]any{"lang": "go"}, Provenance: prov(0)},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: prov(1)},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Attributes: map[string]any{"since": "2025"}, Provenance: prov(1)},
		},
		Chunks: []alchemy.Chunk{
			{Index: 0, Source: "architecture.md", Text: "SuperAI is a system.", Strategy: "heading", Start: 0, End: 20},
			{Index: 1, Source: "architecture.md", Text: "SuperAI uses CortexDB.", Strategy: "heading", Start: 20, End: 42},
		},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownEntityType, Subject: "e1",
			Detail: `entity type "System" is not declared`,
			About:  alchemy.Ref{Kind: alchemy.RefEntity, ID: "e1", Type: "System"},
			// Chunk -1: the finding is about the graph rather than about a span,
			// and a store that filed it under a chunk would be inventing one.
			Provenance: prov(0),
		}},
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "e1 ~ e2",
			Detail: "they may be one node, and nothing joined them",
			Left:   alchemy.DuplicateSide{ID: "e1", Type: "System", Name: "SuperAI", Provenance: prov(0)},
			Right:  alchemy.DuplicateSide{ID: "e2", Type: "System", Name: "CortexDB", Provenance: prov(1)},
		}},
		Guesses: []alchemy.Guess{{
			Field: "customer_id", ChosenAs: "relation:PLACED_BY", Reason: "it names a table", Provenance: prov(0),
		}},
		Unread: []alchemy.Unread{{Source: "scan.pdf", Locator: "page 4", Reason: "no text layer and no OCR model"}},
		// One retirement, and it names a record this result does not contain.
		// That is the ordinary case rather than an awkward one: the thing being
		// superseded is in the store from a run that finished last month, and
		// alchemy.Supersession says plainly that a consumer which cannot find
		// it says so rather than failing. A store that refused this fixture
		// would be refusing the only case the field exists for.
		Supersessions: []alchemy.Supersession{{
			Retires: "e-cto-ada",
			By:      alchemy.Ref{Kind: alchemy.RefEntity, ID: "e1", Type: "System"},
			Reason:  "the office changed hands in March and the old profile still names Ada",
			Provenance: alchemy.Provenance{
				Source: "correction.md", Chunk: -1, Producer: alchemy.ProducerHuman,
				By: "ana@example.com", At: "2026-03-01T00:00:00Z",
			},
		}},
		RuleSets: []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{
			{Name: "authored/violation/type=Flag", Told: "a switch is not an entity, said ana@example.com"},
		}}},
		ModelCalls: []alchemy.ModelCall{{Model: "gemini-3.6-flash-high", Stage: "extract", Calls: 2, Tokens: 900}},
	}
	if dim > 0 {
		for _, c := range res.Chunks {
			v := alchemy.Vector{Chunk: c.Index, Model: "embed-3", Values: make([]float32, dim)}
			for i := range v.Values {
				v.Values[i] = float32(c.Index+1) / float32(i+1)
			}
			res.Vectors = append(res.Vectors, v)
		}
	}
	res.Counts = res.Derivable()
	return res
}

// Corroborated is Graph with one node asserted a second time, by a second
// source, agreeing about what the node is.
//
// The second record is deliberately NOT identical. It agrees about the type and
// the name -- which is what pkg/preflight compares, because those two are what
// a store writes as the node itself -- and differs in its provenance and its
// attributes, which are claims ABOUT the node and are the ordinary thing two
// documents differ on. A fixture whose two records were identical would pass
// against a store that compared whole structs and would prove nothing about the
// rule.
func Corroborated(dim int) alchemy.Result {
	res := Graph(dim)
	second := alchemy.Provenance{
		Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Ontology: "sds@3",
	}
	res.Entities = append(res.Entities, alchemy.Entity{
		ID: "e2", Type: "System", Name: "CortexDB",
		Attributes: map[string]any{"tier": "storage"},
		Provenance: second,
	})
	res.Counts = res.Derivable()
	return res
}
