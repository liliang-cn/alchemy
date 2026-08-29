package tabular

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// DESIGN.md §2.1's first lesson: determinism beats inference wherever it is
// available. A caller who states the mapping has left nothing to infer, so no
// model is called and nothing is guessed — a Guess in this result would be the
// reader claiming credit for a decision the caller made.
func TestSuppliedMappingCallsNoModelAndGuessesNothing(t *testing.T) {
	in := "id,name,total\n1001,First order,42\n1002,Second order,7\n"
	res, err := Read(context.Background(), "orders.csv", strings.NewReader(in), Options{
		Delimiter: ',',
		Mapping: &Mapping{
			EntityType: "Order",
			IDColumn:   "id",
			NameColumn: "name",
			Attributes: map[string]string{"total": "total"},
		},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Guesses) != 0 {
		t.Errorf("Guesses = %+v, want none: the caller stated the mapping", res.Guesses)
	}
	if len(res.ModelCalls) != 0 {
		t.Errorf("ModelCalls = %+v, want none", res.ModelCalls)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("Entities = %+v", res.Entities)
	}
	e := res.Entities[0]
	if e.Type != "Order" || e.Name != "First order" {
		t.Errorf("entity = %+v", e)
	}
	if got := e.Attributes["total"]; got != "42" {
		t.Errorf("total = %v, want 42", got)
	}
	p := e.Provenance
	if p.Producer != alchemy.ProducerTabular || p.Chunk != -1 || p.Source != "orders.csv" {
		t.Errorf("provenance = %+v", p)
	}
	if p.Model != "" || p.Confidence != 0 {
		t.Errorf("provenance = %+v, want no model and no confidence: nothing was inferred", p)
	}
}

// A model that names a column the table does not have has misread the header,
// and every row read under that mapping is wrong in a way the rows cannot show.
// Requirement: never a silently dropped field.
func TestAModelMappingAColumnThatDoesNotExistIsAnError(t *testing.T) {
	llm := &fakeLLM{name: "test-model", reply: `{
		"entity_type": "Order", "id_column": "id",
		"attributes": {"custmer_id": "customer"}, "confidence": 0.9}`}
	_, err := Read(context.Background(), "orders.csv", strings.NewReader("id,customer_id\n1,7\n"), Options{
		Delimiter: ',', LLM: llm,
	})
	if err == nil {
		t.Fatal("want an error for a mapping naming a column the header does not have")
	}
	if !strings.Contains(err.Error(), "custmer_id") || !strings.Contains(err.Error(), "customer_id") {
		t.Errorf("error = %q, want it to name the missing column and show the header", err)
	}
}

// The same rule applies to a caller-supplied mapping. A mapping is a statement
// about one table, and one that does not fit is more likely pointed at the
// wrong file than merely imprecise.
func TestASuppliedMappingNamingAMissingColumnIsAnError(t *testing.T) {
	_, err := Read(context.Background(), "orders.csv", strings.NewReader("id,total\n1,9\n"), Options{
		Delimiter: ',',
		Mapping:   &Mapping{EntityType: "Order", IDColumn: "order_id"},
	})
	if err == nil || !strings.Contains(err.Error(), "order_id") {
		t.Fatalf("err = %v, want it to name the column the header does not have", err)
	}
}

// A column given two roles does not say what it becomes. Letting it be both
// produces an attribute that silently shadows the identity, or an edge from a
// row to itself, and neither is visible in the output.
func TestAColumnMappedTwiceIsAnError(t *testing.T) {
	_, err := Read(context.Background(), "orders.csv", strings.NewReader("id,total\n1,9\n"), Options{
		Delimiter: ',',
		Mapping: &Mapping{
			EntityType: "Order", IDColumn: "id",
			Attributes: map[string]string{"id": "id", "total": "total"},
		},
	})
	if err == nil {
		t.Fatal("want an error: \"id\" is both the id column and an attribute")
	}
	if !strings.Contains(err.Error(), "id") || !strings.Contains(err.Error(), "twice") {
		t.Errorf("error = %q, want it to say the column is mapped twice", err)
	}
}

// Both nil is not a mode. Inventing a mapping out of column order is the one
// thing this package exists not to do.
func TestNoMappingAndNoModelIsAnError(t *testing.T) {
	_, err := Read(context.Background(), "orders.csv", strings.NewReader("id\n1\n"), Options{Delimiter: ','})
	if err == nil || !strings.Contains(err.Error(), "no mapping") {
		t.Fatalf("err = %v, want a refusal naming the missing mapping and model", err)
	}
}

// "EOF" is a true description of an empty source and a useless one.
func TestAnEmptySourceSaysItHasNoHeader(t *testing.T) {
	_, err := Read(context.Background(), "empty.csv", strings.NewReader(""), Options{
		Delimiter: ',', Mapping: idOnly,
	})
	if err == nil || !strings.Contains(err.Error(), "no header") {
		t.Fatalf("err = %v, want it to say the source has no header row", err)
	}
}
