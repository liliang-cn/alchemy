package qdrant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// §5b is a product guarantee, not a debugging aid: "every entity and relation
// carries what produced it — which source, which chunk, deterministic or
// inferred, which model". A store that keeps the edge and drops the rule set
// the model was working under has kept the half that is easy, so this asserts
// on the whole struct rather than on the fields that were easy to write.
func TestEveryFieldOfProvenanceSurvivesTheRoundTrip(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	got, err := l.Records(ctx, Filter{Kinds: []string{"entity", "relation"}}, 0)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(got.Entities) != 2 || len(got.Relations) != 1 {
		t.Fatalf("read back %d entities and %d relations, want 2 and 1", len(got.Entities), len(got.Relations))
	}
	for _, e := range got.Entities {
		want := prov(e.Provenance.Chunk)
		if !reflect.DeepEqual(e.Provenance, want) {
			t.Errorf("entity %s provenance =\n%+v\nwant\n%+v", e.ID, e.Provenance, want)
		}
	}
	if !reflect.DeepEqual(got.Relations[0].Provenance, prov(1)) {
		t.Errorf("relation provenance = %+v, want %+v", got.Relations[0].Provenance, prov(1))
	}
	// The whole record, not only its provenance: an attributes map that came
	// back as nothing would make the round trip a lie about the source's words.
	if !reflect.DeepEqual(got.Relations[0].Attributes, map[string]any{"since": "2025"}) {
		t.Errorf("relation attributes = %+v, want since=2025", got.Relations[0].Attributes)
	}
	// CortexDB sorts before SuperAI, and it is the one the source said nothing
	// else about.
	if got.Entities[0].ID != "CortexDB" || got.Entities[0].Attributes != nil {
		t.Errorf("entity %s has attributes %+v, want CortexDB with nil: the source said nothing and the store should not invent {}",
			got.Entities[0].ID, got.Entities[0].Attributes)
	}
}

// "An agent citing the graph can say which, and a person auditing it can
// filter to the half that was guessed." That sentence is a query, and this is
// it — over indexed keyword fields, so it stays a query when the corpus is
// large.
func TestTheGuessedHalfIsAQuery(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()

	res := smallResult(8)
	// A schema-stated node beside the model-proposed ones, and a model-proposed
	// edge nobody has reviewed.
	res.Entities = append(res.Entities, alchemy.Entity{
		ID: "nodes", Type: "Table", Name: "nodes",
		Provenance: alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"},
	})
	unreviewed := prov(1)
	unreviewed.ReviewedBy = ""
	res.Relations = append(res.Relations, alchemy.Relation{
		From: "SuperAI", To: "nodes", Type: "READS", Provenance: unreviewed,
	})
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	yes, no := true, false
	for name, tc := range map[string]struct {
		filter Filter
		want   int
	}{
		"the guessed half":     {Filter{Kinds: []string{"entity", "relation"}, Inferred: &yes}, 4},
		"the stated half":      {Filter{Kinds: []string{"entity", "relation"}, Inferred: &no}, 1},
		"by producer":          {Filter{Producer: alchemy.ProducerDDL}, 1},
		"by source":            {Filter{Kinds: []string{"entity", "relation"}, Source: "architecture.pdf"}, 4},
		"nobody has reviewed":  {Filter{Kinds: []string{"entity", "relation"}, Reviewed: &no}, 2},
		"somebody reviewed":    {Filter{Kinds: []string{"entity", "relation"}, Reviewed: &yes}, 3},
		"inferred, unreviewed": {Filter{Kinds: []string{"relation"}, Inferred: &yes, Reviewed: &no}, 1},
		"by ontology":          {Filter{Kinds: []string{"entity"}, Ontology: "sds@3"}, 3},
		"by embedding model":   {Filter{Kinds: []string{"entity"}, Model: "gemini-3.6-flash-high"}, 2},
	} {
		n, err := l.Count(ctx, tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n != tc.want {
			t.Errorf("%s: %d records, want %d", name, n, tc.want)
		}
	}
}

// Qdrant will filter on an unindexed payload field by scanning every point,
// which means a missing index is not an error and not a wrong answer — it is
// the same answer, slowly, on the buyer's corpus and not on the test one. So
// the index list is asserted against the server rather than trusted.
func TestEveryFilteredFieldIsPayloadIndexed(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{Dimension: 8})
	ctx := context.Background()
	have, err := l.indexedFields(ctx)
	if err != nil {
		t.Fatalf("payload schema: %v", err)
	}
	for _, idx := range payloadIndexes {
		if !have[idx.field] {
			t.Errorf("payload field %q is filtered on and not indexed; on a real corpus that filter is a full scan", idx.field)
		}
	}
}

// TestProvenanceSurvivesTheRoundTripFieldForField writes every field and reads
// it back.
//
// It is here because two of them did not. provenancePayload wrote prov_by and
// prov_at and readProvenance did not read them, so every record came back out
// of this store with its producer intact and the person who asserted it gone —
// §5b's guarantee inverted on the read side only, which no test that compared
// this package with itself could have seen. A retirement is where it costs the
// most: what a supersession is worth six months later is being able to name
// who said the old answer was over.
func TestProvenanceSurvivesTheRoundTripFieldForField(t *testing.T) {
	want := alchemy.Provenance{
		Source: "architecture.pdf", Chunk: 14, Producer: alchemy.ProducerHuman,
		Model: "m", Ontology: "o", Chunking: "c", Confidence: 0.5,
		ReviewedBy: "rb", RuleSet: "rs", RuledBy: "rd",
		By: "ana@example.com", At: "2026-03-01T00:00:00Z",
	}
	// Through JSON, because that is how a payload actually comes back: a
	// number written as an int arrives as a float64, and a reader that only
	// ever saw the map it wrote would not know.
	raw, err := json.Marshal(provenancePayload(want, map[string]any{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := readProvenance(payload); got != want {
		t.Errorf("provenance did not survive the round trip\n got %+v\nwant %+v", got, want)
	}
}
