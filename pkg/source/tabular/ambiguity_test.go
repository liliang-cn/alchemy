package tabular

import (
	"context"
	"strings"
	"testing"
)

// §2.1 again, from the other side: 两种取法都会跑得干干净净. The same table read
// under the other choice of id column also produces a clean graph — a different
// one, with the same row count and nothing to show which is right. What tells
// the two runs apart is not the entities; it is the guess.
func TestTheOtherChoiceOfIDColumnAlsoRunsCleanlyAndAlsoSaysSo(t *testing.T) {
	const in = "id,order_id,product_id,qty\n7,1001,55,2\n"
	byLineID := &fakeLLM{name: "m", reply: `{"entity_type":"LineItem","id_column":"id",
		"relations":[{"column":"order_id","relation_type":"PART_OF","target_type":"Order"}],
		"confidence":0.7}`}
	byOrderID := &fakeLLM{name: "m", reply: `{"entity_type":"LineItem","id_column":"order_id",
		"relations":[{"column":"id","relation_type":"PART_OF","target_type":"Order"}],
		"confidence":0.7}`}

	a, err := Read(context.Background(), "line_items.csv", strings.NewReader(in), Options{Delimiter: ',', LLM: byLineID})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	b, err := Read(context.Background(), "line_items.csv", strings.NewReader(in), Options{Delimiter: ',', LLM: byOrderID})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(a.Entities) != len(b.Entities) || len(a.Violations) != 0 || len(b.Violations) != 0 {
		t.Fatalf("one of the two readings did not run cleanly: %+v / %+v", a, b)
	}
	if a.Entities[0].ID == b.Entities[0].ID {
		t.Fatalf("the two readings produced the same graph; the test no longer says anything")
	}
	for _, res := range []Result{a, b} {
		var found bool
		for _, g := range res.Guesses {
			if g.ChosenAs != "id_column" {
				continue
			}
			found = true
			if len(g.Alternatives) < 2 {
				t.Errorf("id guess = %+v, want the columns it was chosen over", g)
			}
		}
		if !found {
			t.Errorf("no id_column guess in %+v", res.Guesses)
		}
	}
}

// Ambiguity is reported as a guess rather than refused, because refusing would
// make this reader useless on the ordinary table — every table with an id and
// two foreign keys has this shape. A caller who wants the refusal already has
// it: supply Options.Mapping and nothing is inferred at all. What is refused is
// the third option, resolving it silently.
func TestAnAmbiguousIDColumnIsAGuessRatherThanARefusal(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"entity_type":"LineItem","id_column":"id","confidence":0.4}`}
	res, err := Read(context.Background(), "line_items.csv",
		strings.NewReader("id,order_id,product_id\n7,1001,55\n"), Options{Delimiter: ',', LLM: llm})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	g, ok := guessFor(res, "id", "id_column")
	if !ok {
		t.Fatalf("guesses = %+v", res.Guesses)
	}
	if !strings.Contains(g.Reason, "substring") || !strings.Contains(g.Reason, "column order") {
		t.Errorf("Reason = %q, want it to name the shape of the ambiguity", g.Reason)
	}
}

// With no id column named there is nothing to fall back on. Taking the first
// column, or the one whose name contains "id", is the positional guess this
// package refuses to make.
func TestAMappingWithNoIDColumnIsRefusedRatherThanGuessedAt(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"entity_type":"Order","attributes":{"total":"total"},"confidence":0.9}`}
	_, err := Read(context.Background(), "orders.csv", strings.NewReader("id,total\n1,9\n"),
		Options{Delimiter: ',', LLM: llm})
	if err == nil || !strings.Contains(err.Error(), "no id column") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// The caller's hint is a statement, not an inference. A model that names no
// entity type has left the deterministic answer standing, and §2.1's first
// lesson says to take it.
func TestTheCallersHintStandsInForATypeTheModelDidNotName(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"id_column":"id","confidence":0.9}`}
	res, err := Read(context.Background(), "orders.csv", strings.NewReader("id,total\n1,9\n"),
		Options{Delimiter: ',', LLM: llm, EntityHint: "Order"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].Type != "Order" || res.Entities[0].ID != "order:1" {
		t.Fatalf("entities = %+v", res.Entities)
	}
	g, ok := guessFor(res, "orders.csv", "Order")
	if !ok || !strings.Contains(g.Reason, "hint") {
		t.Errorf("entity-type guess = %+v, want it to say the hint was used", g)
	}
}

// A relation whose target is in another file is still an edge. A table is a
// fragment of a graph, unlike a schema, so a reference out of it is expected
// rather than dangling — the verifier resolves it once the other sources are in.
func TestARelationMayPointOutsideThisTable(t *testing.T) {
	res, err := readFixed(t, "id,customer_id\n1,7\n", &Mapping{
		EntityType: "Order", IDColumn: "id",
		Relations: []RelationMapping{{Column: "customer_id", RelationType: "PLACED_BY", TargetType: "Customer"}},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Relations) != 1 || res.Relations[0].To != "customer:7" {
		t.Fatalf("relations = %+v", res.Relations)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none: the customer is in another file", res.Violations)
	}
}
