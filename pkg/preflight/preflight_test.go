package preflight_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/preflight"
)

// clean is a small result that is true about itself, so that every test below
// is about the one thing it breaks.
func clean() alchemy.Result {
	res := alchemy.Result{
		Job: "job-1",
		Entities: []alchemy.Entity{
			{ID: "cluster:superai", Type: "Cluster", Name: "SuperAI",
				Provenance: alchemy.Provenance{Source: "a.md", Chunk: 0, Producer: alchemy.ProducerLLMExtract}},
			{ID: "db:cortexdb", Type: "Database", Name: "CortexDB",
				Provenance: alchemy.Provenance{Source: "a.md", Chunk: 1, Producer: alchemy.ProducerLLMExtract}},
		},
		Relations: []alchemy.Relation{
			{From: "cluster:superai", To: "db:cortexdb", Type: "USES",
				Provenance: alchemy.Provenance{Source: "a.md", Chunk: 1, Producer: alchemy.ProducerLLMExtract}},
		},
		Chunks: []alchemy.Chunk{
			{Index: 0, Source: "a.md", Text: "SuperAI is a cluster."},
			{Index: 1, Source: "a.md", Text: "SuperAI uses CortexDB."},
		},
		Vectors: []alchemy.Vector{
			{Chunk: 0, Values: []float32{1, 0}, Model: "e5"},
			{Chunk: 1, Values: []float32{0, 1}, Model: "e5"},
		},
	}
	res.Counts = res.Derivable()
	return res
}

func kinds(ds []preflight.Defect) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, string(d.Kind))
	}
	return out
}

func has(ds []preflight.Defect, kind preflight.Kind) *preflight.Defect {
	for i := range ds {
		if ds[i].Kind == kind {
			return &ds[i]
		}
	}
	return nil
}

// The baseline every other test leans on: a result that is true about itself
// is not accused of anything. A checker that cries wolf on a clean graph is one
// nobody wires in, and then it checks nothing.
func TestAResultThatIsTrueAboutItselfHasNoDefects(t *testing.T) {
	if ds := preflight.Check(clean()); len(ds) != 0 {
		t.Fatalf("Check() = %v, want nothing", kinds(ds))
	}
	if err := preflight.Refuse(clean()); err != nil {
		t.Fatalf("Refuse() = %v, want nil", err)
	}
}

// §7.3, and the reason it is checked here rather than left to each store: a
// graph that contradicts itself must not reach one. Four stores each reached
// for this rule; the fifth is why it is in a package a writer already has to
// call.
func TestAHeldResultIsRefused(t *testing.T) {
	res := clean()
	res.Conflicts = []alchemy.Conflict{{Kind: alchemy.ConflictEntityType, Subject: "cluster:superai"}}
	res.Counts = res.Derivable()

	d := has(preflight.Check(res), preflight.Held)
	if d == nil {
		t.Fatalf("Check() = %v, want a held defect", kinds(preflight.Check(res)))
	}
	if d.Severity != preflight.SeverityRefuse {
		t.Errorf("severity = %q, want a refusal: a held graph must not be written", d.Severity)
	}
	if err := preflight.Refuse(res); err == nil {
		t.Fatal("Refuse() = nil for a held result")
	}
}

// The invariant pkg/alchemy never stated and only one of four stores defended.
// Two files' first chunks both numbered 0 derive one row, the second overwrites
// the first, and the load reports two chunks where the store holds one.
func TestTwoChunksUnderOneIndexAreRefusedAndBothSourcesAreNamed(t *testing.T) {
	res := clean()
	res.Chunks = append(res.Chunks, alchemy.Chunk{Index: 1, Source: "b.md", Text: "another file"})
	res.Counts = res.Derivable()

	d := has(preflight.Check(res), preflight.ChunkIndexReused)
	if d == nil {
		t.Fatalf("Check() = %v, want a reused chunk index", kinds(preflight.Check(res)))
	}
	if d.Severity != preflight.SeverityRefuse {
		t.Errorf("severity = %q, want a refusal", d.Severity)
	}
	// Both sources, because "chunk 1 is ambiguous" sends a reader nowhere and
	// "a.md and b.md both call something chunk 1" sends them to the join.
	for _, want := range []string{"a.md", "b.md"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("detail = %q, want it to name %q", d.Detail, want)
		}
	}
}

// Entity.ID is what relations refer to. Two entities under one ID means every
// edge naming it points at whichever of them the store wrote last.
func TestTwoEntitiesUnderOneIDAreRefused(t *testing.T) {
	res := clean()
	res.Entities = append(res.Entities, alchemy.Entity{
		ID: "cluster:superai", Type: "Node", Name: "SuperAI",
		Provenance: alchemy.Provenance{Source: "b.md", Chunk: 1},
	})
	res.Counts = res.Derivable()

	if has(preflight.Check(res), preflight.EntityIDReused) == nil {
		t.Fatalf("Check() = %v, want a reused entity ID", kinds(preflight.Check(res)))
	}
}

// A vector naming a chunk the result does not carry is an embedding with no
// text behind it. Both vector stores wrote this check; both graph stores were
// exposed to it.
func TestAVectorNamingNoChunkIsRefused(t *testing.T) {
	res := clean()
	res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: 9, Values: []float32{1, 1}, Model: "e5"})
	res.Counts = res.Derivable()

	if has(preflight.Check(res), preflight.VectorWithoutChunk) == nil {
		t.Fatalf("Check() = %v, want a vector with no chunk", kinds(preflight.Check(res)))
	}
}

// One run, one width: an index takes vectors of a single dimension, and there
// is nothing in the data to say which width was the one meant.
func TestVectorsOfTwoWidthsAreRefused(t *testing.T) {
	res := clean()
	res.Vectors[1].Values = []float32{0, 1, 0}
	res.Counts = res.Derivable()

	d := has(preflight.Check(res), preflight.VectorWidth)
	if d == nil {
		t.Fatalf("Check() = %v, want two widths", kinds(preflight.Check(res)))
	}
	if !strings.Contains(d.Detail, "2") || !strings.Contains(d.Detail, "3") {
		t.Errorf("detail = %q, want both widths", d.Detail)
	}
}

// A vector with no dimensions is well-formed enough to be stored and searched
// against, and then matches everything or nothing depending on the index.
func TestAnEmptyVectorIsRefused(t *testing.T) {
	res := clean()
	res.Vectors[0].Values = nil
	res.Counts = res.Derivable()

	if has(preflight.Check(res), preflight.VectorEmpty) == nil {
		t.Fatalf("Check() = %v, want an empty vector", kinds(preflight.Check(res)))
	}
}

// Two vectors for one chunk is the same silent last-writer-wins as two chunks
// under one index, one layer along, and neither vector store caught it.
func TestTwoVectorsForOneChunkAreRefused(t *testing.T) {
	res := clean()
	res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: 0, Values: []float32{1, 1}, Model: "e5"})
	res.Counts = res.Derivable()

	if has(preflight.Check(res), preflight.ChunkVectoredTwice) == nil {
		t.Fatalf("Check() = %v, want one chunk embedded twice", kinds(preflight.Check(res)))
	}
}

// §8.4 pages a large result, so a result with entities and no chunks is an
// ordinary shape rather than a defect — and a DDL job has no chunks at all.
// The dangling check is about a result that carries chunks and gets them wrong.
func TestAResultWithNoChunksIsNotAccusedOfLosingThem(t *testing.T) {
	res := alchemy.Result{
		Entities: []alchemy.Entity{{ID: "table:users", Type: "Table", Name: "users",
			Provenance: alchemy.Provenance{Source: "s.sql", Chunk: -1, Producer: alchemy.ProducerDDL}}},
	}
	res.Counts = res.Derivable()
	if ds := preflight.Check(res); len(ds) != 0 {
		t.Fatalf("Check() = %v, want nothing: a schema import has no chunks", kinds(ds))
	}
}

// A record citing a chunk the result carries chunks for and does not carry
// that one. §5b's citation resolves to nothing, and only something holding
// both halves can notice.
func TestARecordCitingAChunkThatIsNotThereIsReported(t *testing.T) {
	res := clean()
	res.Entities[0].Provenance.Chunk = 7
	res.Counts = res.Derivable()

	d := has(preflight.Check(res), preflight.ProvenanceWithoutChunk)
	if d == nil {
		t.Fatalf("Check() = %v, want a citation that resolves to nothing", kinds(preflight.Check(res)))
	}
	// Not a refusal: the record is still writable and still attributable to its
	// source. What is lost is the chunk half of the citation, which is a thing
	// to report rather than a reason to throw the graph away.
	if d.Severity != preflight.SeverityReport {
		t.Errorf("severity = %q, want a report", d.Severity)
	}
}

// §5's obligation is numbers a reader can distrust the graph with, and until
// now they were a claim no consumer could test. All four stores wrote them
// down verbatim; one wrote its own tally beside them because the two could
// disagree and it had no way to say which was right.
func TestCountsThatDisagreeWithTheSlicesAreReported(t *testing.T) {
	res := clean()
	res.Counts.Relations = 99

	d := has(preflight.Check(res), preflight.CountsDisagree)
	if d == nil {
		t.Fatalf("Check() = %v, want the counts to be caught", kinds(preflight.Check(res)))
	}
	if !strings.Contains(d.Detail, "relations") || !strings.Contains(d.Detail, "99") {
		t.Errorf("detail = %q, want the field and the claim", d.Detail)
	}
}

// ChunksEmpty and Dropped cannot be recomputed — a chunk that produced nothing
// and a record a rule removed both leave nothing behind to count — so a
// checker that compared them would report a defect on every honest result.
func TestTheTwoCountsThatCannotBeRecomputedAreNotChecked(t *testing.T) {
	res := clean()
	res.Counts.ChunksEmpty = 3
	res.Counts.Dropped = 4

	if d := has(preflight.Check(res), preflight.CountsDisagree); d != nil {
		t.Fatalf("Check() reported %q; those two are not derivable and must not be compared", d.Detail)
	}
}

// §5c: Provenance.RuleSet is a name into Result.RuleSets, so a result that
// carries the names and leaves the sets behind gives every record a pointer
// into nothing. One store shipped exactly that and its own graph still does.
func TestARuleSetNameThatResolvesToNothingIsReported(t *testing.T) {
	res := clean()
	res.Entities[0].Provenance.RuleSet = "rs-9f21"
	res.Counts = res.Derivable()

	d := has(preflight.Check(res), preflight.RuleSetUnresolved)
	if d == nil {
		t.Fatalf("Check() = %v, want an unresolved rule set", kinds(preflight.Check(res)))
	}
	if !strings.Contains(d.Detail, "rs-9f21") {
		t.Errorf("detail = %q, want the name that resolves to nothing", d.Detail)
	}
}

// RuledBy names one rule inside a set, by the name the set uses for it, and it
// is as resolvable as the set name is.
func TestARuledByNameThatResolvesToNothingIsReported(t *testing.T) {
	res := clean()
	res.RuleSets = []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{
		{Name: "authored/violation/type=Flag", Told: "a switch is not an entity, said ana"},
	}}}
	res.Entities[0].Provenance.RuleSet = "rs-9f21"
	res.Entities[0].Provenance.RuledBy = "authored/violation/type=Ghost"
	res.Counts = res.Derivable()

	if has(preflight.Check(res), preflight.RuleUnresolved) == nil {
		t.Fatalf("Check() = %v, want an unresolved rule", kinds(preflight.Check(res)))
	}
}

// A name that does resolve is not a defect, which is the half that proves the
// check is looking at the sets rather than at the presence of a name.
func TestARuleSetNameThatResolvesIsFine(t *testing.T) {
	res := clean()
	res.RuleSets = []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{
		{Name: "authored/violation/type=Flag", Told: "a switch is not an entity, said ana"},
	}}}
	res.Entities[0].Provenance.RuleSet = "rs-9f21"
	res.Entities[0].Provenance.RuledBy = "authored/violation/type=Flag"
	res.Counts = res.Derivable()

	if ds := preflight.Check(res); len(ds) != 0 {
		t.Fatalf("Check() = %v, want nothing: the record's policy is in the result", kinds(ds))
	}
}

// The attribute value domain is the JSON one (§4). A producer that puts a Go
// value outside it in there breaks every consumer at once and quietly: it
// round-trips inside this process and changes type on the way out of it.
func TestAnAttributeValueOutsideTheJSONDomainIsReported(t *testing.T) {
	res := clean()
	res.Entities[0].Attributes = map[string]any{"started": struct{ A int }{1}}
	res.Counts = res.Derivable()

	d := has(preflight.Check(res), preflight.AttributeType)
	if d == nil {
		t.Fatalf("Check() = %v, want the attribute caught", kinds(preflight.Check(res)))
	}
	if !strings.Contains(d.Detail, "started") {
		t.Errorf("detail = %q, want the attribute named", d.Detail)
	}
}

// Nested objects and arrays are in the domain, at any depth, and a checker
// that refused them would be refusing what the extractor legitimately produces
// out of a model's JSON reply.
func TestNestedJSONValuesAreInTheDomain(t *testing.T) {
	res := clean()
	res.Entities[0].Attributes = map[string]any{
		"ports":   []any{float64(80), float64(443)},
		"address": map[string]any{"city": "Wien", "verified": true, "note": nil},
	}
	res.Counts = res.Derivable()

	if ds := preflight.Check(res); len(ds) != 0 {
		t.Fatalf("Check() = %v, want nothing: nested JSON is what a model replies with", kinds(ds))
	}
}

// Refuse names every blocking defect rather than the first, because a writer
// that fixes one and re-runs to find the next is a writer running an import to
// get an error message.
func TestRefuseNamesEveryBlockingDefectAtOnce(t *testing.T) {
	res := clean()
	res.Chunks = append(res.Chunks, alchemy.Chunk{Index: 1, Source: "b.md"})
	res.Entities = append(res.Entities, alchemy.Entity{ID: "db:cortexdb", Type: "Node", Name: "CortexDB"})
	res.Counts = res.Derivable()

	err := preflight.Refuse(res)
	if err == nil {
		t.Fatal("Refuse() = nil, want both defects")
	}
	for _, want := range []string{"chunk_index_reused", "entity_id_reused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Refuse() = %q, want it to name %s", err, want)
		}
	}
}

// Refuse is Check filtered, not a second implementation. A store calling one
// and a report reading the other must never disagree about whether a graph is
// writable.
func TestRefuseReportsExactlyTheBlockingDefectsCheckFound(t *testing.T) {
	res := clean()
	res.Chunks = append(res.Chunks, alchemy.Chunk{Index: 1, Source: "b.md"})
	res.Counts.Guesses = 12 // a report, not a refusal

	err := preflight.Refuse(res)
	if err == nil {
		t.Fatal("Refuse() = nil")
	}
	if strings.Contains(err.Error(), string(preflight.CountsDisagree)) {
		t.Errorf("Refuse() = %q; a wrong count does not stop a write", err)
	}
	if has(preflight.Check(res), preflight.CountsDisagree) == nil {
		t.Error("Check() lost the count defect Refuse correctly ignored")
	}
}
