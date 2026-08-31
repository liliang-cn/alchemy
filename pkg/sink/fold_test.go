package sink_test

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Two records under one ID that agree about the node are one node asserted
// twice, and every store keys entities by ID, so exactly one of them can be a
// row. Which one, and whether anybody is told, was answered four different ways
// before this: MERGE kept the last, an upsert kept the last silently, a primary
// key would have failed at the database, and a triple store put both sources on
// one annotation with nothing saying which went with which.
//
// The first is kept, because pkg/preflight reports the pair as "asserted by A
// and by B" with A the one it saw first, and a store that kept B would be
// keeping the record the report names second.
func TestRecordsThatAgreeAboutOneNodeArriveAsOne(t *testing.T) {
	r := &recorder{}
	res := graph()
	first := res.Entities[1]
	second := first
	second.Provenance = alchemy.Provenance{
		Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport,
	}
	second.Attributes = map[string]any{"tier": "storage"}
	res.Entities = append(res.Entities, second)
	res.Counts = res.Derivable()

	rep, err := sink.Load(context.Background(), r, res, sink.Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.entities) != 2 {
		t.Fatalf("the store was handed %d entities, want 2: three records under two IDs is two nodes", len(r.entities))
	}
	if got := r.entities[1]; got.Provenance != first.Provenance {
		t.Errorf("the store was handed provenance %+v, want the FIRST record's %+v", got.Provenance, first.Provenance)
	}
	if rep.Entities != 2 {
		t.Errorf("Report.Entities = %d, want 2: it counts what was handed over", rep.Entities)
	}
	// Reported rather than silent, because what the fold costs -- the second
	// record's provenance -- cannot be recovered from the row it folded into.
	if rep.Corroborated != 1 {
		t.Errorf("Report.Corroborated = %d, want 1", rep.Corroborated)
	}
}

// The fold must not reach across IDs. A store handed a graph in which nothing
// repeats has to be handed all of it, and Corroborated has to be zero rather
// than merely small -- a count that was nearly right would read as "this graph
// came from more than one source" about a graph that did not.
func TestAGraphWithNoRepeatedIDIsHandedOverWhole(t *testing.T) {
	r := &recorder{}
	res := graph()
	rep, err := sink.Load(context.Background(), r, res, sink.Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.entities) != len(res.Entities) || rep.Corroborated != 0 {
		t.Errorf("handed over %d of %d entities with Corroborated = %d, want all of them and 0",
			len(r.entities), len(res.Entities), rep.Corroborated)
	}
}
