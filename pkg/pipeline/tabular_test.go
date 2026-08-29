package pipeline

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/source/tabular"
)

const ordersCSV = "id,customer_id,total\n1,c1,10\n2,c2,20\n"

// §2.1's determinism-first rule applied to the one reader that can go either
// way: a mapping the caller states is a mapping nobody guessed, so no model is
// called and there is nothing to report as a Guess.
func TestACallerSuppliedMappingBuysNoInference(t *testing.T) {
	req := Request{
		Sources: []Source{{Name: "orders.csv", Kind: alchemy.SourceTabular, Open: openString(ordersCSV)}},
		Models:  alchemy.Models{LLM: &failLLM{t: t}},
		Mapping: &tabular.Mapping{
			EntityType: "Order",
			IDColumn:   "id",
			Attributes: map[string]string{"total": "total"},
			Relations: []tabular.RelationMapping{
				{Column: "customer_id", RelationType: "PLACED_BY", TargetType: "Customer"},
			},
		},
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("want an edge per row, got %d: %+v", len(res.Relations), res.Relations)
	}
	if len(res.Guesses) != 0 {
		t.Errorf("a stated mapping produced %d guesses: %+v", len(res.Guesses), res.Guesses)
	}
	if len(res.ModelCalls) != 0 {
		t.Errorf("ModelCalls = %+v, want none", res.ModelCalls)
	}
}

// The other mode, and the reason Result.Guesses is not optional: "一个猜错的
// 映射不会报错，它只会让一整张表对不上账，然后在三个月后由一个人手工发现."
// The mapping was inferred, so every decision in it is reported, and the call
// it cost is in the cost report under its own stage.
func TestAnInferredMappingIsReportedAsAGuess(t *testing.T) {
	llm := &scriptLLM{name: "mapper-1", replies: map[string]string{
		"customer_id": `{"entity_type":"Order","id_column":"id",
		  "attributes":{"total":"total"},
		  "relations":[{"column":"customer_id","relation_type":"PLACED_BY","target_type":"Customer"}],
		  "confidence":0.7,
		  "reasons":{"id_column":"id names the row; customer_id names a customer"}}`,
	}}
	req := Request{
		Sources: []Source{{Name: "orders.csv", Kind: alchemy.SourceTabular, Open: openString(ordersCSV)}},
		Models:  alchemy.Models{LLM: llm},
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Guesses) == 0 {
		t.Fatal("an inferred mapping reported no guesses")
	}
	var mapping bool
	for _, c := range res.ModelCalls {
		if c.Stage == "tabular-mapping" && c.Model == "mapper-1" && c.Calls == 1 {
			mapping = true
		}
	}
	if !mapping {
		t.Errorf("ModelCalls = %+v, want the mapping call under its own stage", res.ModelCalls)
	}
}
