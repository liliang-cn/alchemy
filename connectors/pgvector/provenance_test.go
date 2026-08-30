package pgvector

import (
	"context"
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// §5b makes "every entity and relation can name the source, the chunk and the
// producer it came from" a product guarantee rather than a debugging aid. A
// connector is where that guarantee is most easily lost, because the graph
// survives a lossy load and looks fine — the edge is still there, and only the
// question "under what policy did the model say this?" stops having an answer.
//
// So the test is a whole-struct comparison rather than a field-by-field one:
// reflect.DeepEqual on alchemy.Provenance fails when a field is added to the
// type and forgotten here, which is the failure mode that matters.
func TestProvenanceSurvivesARoundTrip(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()

	res := smallResult(8)
	// A deterministic producer beside the inferred ones: the split is what §5b
	// says an auditor filters on, so both sides have to make it through.
	res.Entities = append(res.Entities, alchemy.Entity{
		ID: "orders", Type: "Table", Name: "orders",
		Provenance: alchemy.Provenance{
			Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3",
		},
	})
	loaded, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	g, err := l.Graph(ctx, loaded.ID)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if len(g.Entities) != 3 || len(g.Relations) != 1 {
		t.Fatalf("read back %d entities and %d relations, want 3 and 1", len(g.Entities), len(g.Relations))
	}
	byID := map[string]alchemy.Entity{}
	for _, e := range g.Entities {
		byID[e.ID] = e
	}
	for _, want := range res.Entities {
		got, ok := byID[want.ID]
		if !ok {
			t.Fatalf("entity %s did not come back", want.ID)
		}
		if !reflect.DeepEqual(got.Provenance, want.Provenance) {
			t.Errorf("entity %s provenance:\n got %+v\nwant %+v", want.ID, got.Provenance, want.Provenance)
		}
		if got.Type != want.Type || got.Name != want.Name {
			t.Errorf("entity %s = %q/%q, want %q/%q", want.ID, got.Type, got.Name, want.Type, want.Name)
		}
		if !reflect.DeepEqual(got.Attributes, want.Attributes) {
			t.Errorf("entity %s attributes = %#v, want %#v", want.ID, got.Attributes, want.Attributes)
		}
	}
	edge := g.Relations[0]
	if !reflect.DeepEqual(edge.Provenance, res.Relations[0].Provenance) {
		t.Errorf("edge provenance:\n got %+v\nwant %+v", edge.Provenance, res.Relations[0].Provenance)
	}
	if edge.From != "SuperAI" || edge.To != "CortexDB" || edge.Type != "USES" {
		t.Errorf("edge = %s -[%s]-> %s, want SuperAI -[USES]-> CortexDB", edge.From, edge.Type, edge.To)
	}
}

// The columns are the product too, not only the Go round trip: a buyer's own
// SQL is the reason to load into their store rather than keep the JSON, and
// "filter to the half that was guessed" has to be a WHERE clause.
func TestProvenanceIsQueryableAsColumns(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	res.Entities = append(res.Entities, alchemy.Entity{
		ID: "orders", Type: "Table", Name: "orders",
		Provenance: alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL},
	})
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	var inferred, deterministic int
	f.scalar(t, &inferred, `SELECT count(*) FROM {s}.loaded_entities WHERE NOT prov_deterministic`)
	f.scalar(t, &deterministic, `SELECT count(*) FROM {s}.loaded_entities WHERE prov_deterministic`)
	if inferred != 2 || deterministic != 1 {
		t.Errorf("inferred/deterministic = %d/%d, want 2/1", inferred, deterministic)
	}

	// The edge, whole, as a person auditing it would ask.
	var producer, model, ontology, ruleSet, ruledBy, reviewedBy string
	var chunk int
	f.scalar(t, &producer, `SELECT prov_producer FROM {s}.loaded_relations`)
	f.scalar(t, &model, `SELECT prov_model FROM {s}.loaded_relations`)
	f.scalar(t, &ontology, `SELECT prov_ontology FROM {s}.loaded_relations`)
	f.scalar(t, &ruleSet, `SELECT prov_rule_set FROM {s}.loaded_relations`)
	f.scalar(t, &ruledBy, `SELECT prov_ruled_by FROM {s}.loaded_relations`)
	f.scalar(t, &reviewedBy, `SELECT prov_reviewed_by FROM {s}.loaded_relations`)
	f.scalar(t, &chunk, `SELECT prov_chunk FROM {s}.loaded_relations`)
	for _, c := range []struct{ name, got, want string }{
		{"producer", producer, "llm-extract"},
		{"model", model, "gemini-3.6-flash-high"},
		{"ontology", ontology, "sds@3"},
		{"rule_set", ruleSet, "rs-9f21"},
		{"ruled_by", ruledBy, "authored/type:Service"},
		{"reviewed_by", reviewedBy, "ada@example.com"},
	} {
		if c.got != c.want {
			t.Errorf("edge %s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if chunk != 1 {
		t.Errorf("edge chunk = %d, want 1", chunk)
	}
}

// "No attributes" and "an empty object of attributes" are different things a
// source can say, and a store that renders both as '{}' has decided one of them
// never happens.
func TestAbsentAttributesStayAbsent(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	res.Entities[1].Attributes = map[string]any{}
	loaded, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var nulls int
	f.scalar(t, &nulls, `SELECT count(*) FROM {s}.loaded_entities WHERE attributes IS NULL`)
	if nulls != 0 {
		t.Errorf("%d NULL attribute columns, want 0: this result gave both entities attributes", nulls)
	}
	g, err := l.Graph(ctx, loaded.ID)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	for _, e := range g.Entities {
		if e.Attributes == nil {
			t.Errorf("entity %s came back with nil attributes; it was stored with an empty map", e.ID)
		}
	}
}
