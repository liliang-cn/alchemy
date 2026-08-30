package tabular

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// This test is DESIGN.md §2.1's second lesson, written down so that nobody
// deletes it:
//
//	id 同时是 order_id 和 product_id 的子串，取哪一个只看列在源里的先后顺序，
//	而两种取法都会跑得干干净净。
//
// Both readings run cleanly, which is why the reader is not allowed to just
// pick one: the choice is reported with the columns it was chosen over. Delete
// this test and the failure it guards against reappears silently, three months
// before anyone notices.
func TestTheIDColumnIsAGuessBecauseItIsASubstringOfOrderIDAndProductID(t *testing.T) {
	llm := &fakeLLM{name: "test-model", reply: `{
		"entity_type": "LineItem",
		"id_column": "id",
		"name_column": "",
		"attributes": {"qty": "qty"},
		"relations": [
			{"column": "order_id", "relation_type": "PART_OF", "target_type": "Order"},
			{"column": "product_id", "relation_type": "OF_PRODUCT", "target_type": "Product"}
		],
		"confidence": 0.7
	}`}
	in := "id,order_id,product_id,qty\n7,1001,55,2\n"
	res, err := Read(context.Background(), "line_items.csv", strings.NewReader(in), Options{
		Delimiter: ',', LLM: llm,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	g, ok := guessFor(res, "id", "id_column")
	if !ok {
		t.Fatalf("no guess for the id column; guesses = %+v", res.Guesses)
	}
	if !hasAll(g.Alternatives, []string{"order_id", "product_id"}) {
		t.Errorf("Alternatives = %v, want order_id and product_id: a guess that "+
			"names no alternative tells a reviewer nothing", g.Alternatives)
	}
	if g.Reason == "" {
		t.Error("Reason is empty")
	}
	if g.Provenance.Model != "test-model" || g.Provenance.Producer != alchemy.ProducerTabular {
		t.Errorf("guess provenance = %+v", g.Provenance)
	}
	// The row first, then the two things its columns name. Both readings of the
	// id column still produce the same count, which is why the guess above and
	// not the graph is what tells them apart.
	if len(res.Entities) != 3 || res.Entities[0].ID != "lineitem:7" {
		t.Fatalf("entities = %+v, want the row identified by the id column and the two it names", res.Entities)
	}
	if p := res.Entities[0].Provenance; p.Model != "test-model" || p.Confidence != 0.7 {
		t.Errorf("entity provenance = %+v, want the model and its confidence", p)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("relations = %+v", res.Relations)
	}
	if res.Relations[0].From != "lineitem:7" || res.Relations[0].To != "order:1001" {
		t.Errorf("relation = %+v, want it to refer to the entity's stable id", res.Relations[0])
	}
	// Every edge lands on something this result contains. Before the reader
	// created the entities its mapping named, both of these were dangling.
	have := map[string]bool{}
	for _, e := range res.Entities {
		have[e.ID] = true
	}
	for _, rel := range res.Relations {
		if !have[rel.To] {
			t.Errorf("relation %+v points at an entity this result does not contain", rel)
		}
	}
}

// DESIGN.md §7.2: cost is not optimised for, but it is never hidden. A call
// that failed was still made, so the error carries the result that records it —
// otherwise a job that retries a table three times reports the price of one.
func TestAFailedMappingCallIsStillReported(t *testing.T) {
	llm := &fakeLLM{name: "test-model", err: errors.New("upstream is down")}
	res, err := Read(context.Background(), "orders.csv", strings.NewReader("id\n1\n"), Options{
		Delimiter: ',', LLM: llm,
	})
	if err == nil {
		t.Fatal("want the model's error")
	}
	if len(res.ModelCalls) != 1 || res.ModelCalls[0].Model != "test-model" || res.ModelCalls[0].Calls != 1 {
		t.Fatalf("ModelCalls = %+v, want the call that was made and failed", res.ModelCalls)
	}
	if res.ModelCalls[0].Stage != inferStage {
		t.Errorf("Stage = %q", res.ModelCalls[0].Stage)
	}
}

// A reply that is not a mapping is an error, not an empty graph: a table that
// produced no entities because the model returned prose looks exactly like a
// table that was empty.
func TestAReplyThatIsNotJSONIsAnError(t *testing.T) {
	llm := &fakeLLM{name: "test-model", reply: "I'm afraid I can't do that."}
	_, err := Read(context.Background(), "orders.csv", strings.NewReader("id\n1\n"), Options{
		Delimiter: ',', LLM: llm,
	})
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("err = %v, want it to say the reply was not a mapping", err)
	}
}

// Models fence their JSON. Failing a whole table over a decoration would be a
// refusal that teaches callers to pre-process rather than one that finds bugs.
func TestAFencedReplyIsAccepted(t *testing.T) {
	llm := &fakeLLM{name: "test-model", reply: "```json\n{\"entity_type\":\"Order\",\"id_column\":\"id\",\"confidence\":0.5}\n```"}
	res, err := Read(context.Background(), "orders.csv", strings.NewReader("id\n1\n"), Options{
		Delimiter: ',', LLM: llm,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "order:1" {
		t.Fatalf("entities = %+v", res.Entities)
	}
}

// The prompt is the only place the model learns what this table is. It shows
// the columns in order and values under them, because a header alone cannot
// tell an identifier from a quantity.
func TestThePromptShowsTheColumnsInOrderAndRowsUnderThem(t *testing.T) {
	llm := &fakeLLM{name: "test-model", reply: `{"entity_type":"Order","id_column":"id","confidence":0.5}`}
	_, err := Read(context.Background(), "orders.csv", strings.NewReader("id,total\n1001,42\n1002,7\n"), Options{
		Delimiter: ',', LLM: llm, EntityHint: "Order",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"orders.csv", "Order", " 1. id", " 2. total", "1001 | 42"} {
		if !strings.Contains(llm.asked.Prompt, want) {
			t.Errorf("prompt does not contain %q:\n%s", want, llm.asked.Prompt)
		}
	}
	if !strings.Contains(llm.asked.System, "verbatim") || !llm.asked.JSON {
		t.Errorf("system = %q, JSON = %v", llm.asked.System, llm.asked.JSON)
	}
}

// Every decision the model made is a guess, and a guess whose Alternatives are
// empty tells a reviewer nothing. For an identifier-shaped column there is
// always something to say: it could have been the id.
func TestEveryIdentifierShapedColumnNamesWhatElseItCouldHaveBeen(t *testing.T) {
	llm := &fakeLLM{name: "test-model", reply: `{
		"entity_type": "Order", "id_column": "id", "name_column": "reference",
		"attributes": {"total": "total"},
		"relations": [{"column": "customer_id", "relation_type": "PLACED_BY", "target_type": "Customer"}],
		"confidence": 0.6}`}
	res, err := Read(context.Background(), "orders.csv",
		strings.NewReader("id,reference,customer_id,total\n1,ORD-1,7,42\n"), Options{Delimiter: ',', LLM: llm})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	g, ok := guessFor(res, "customer_id", "relation:PLACED_BY->Customer")
	if !ok {
		t.Fatalf("no guess for customer_id; guesses = %+v", res.Guesses)
	}
	if !hasAll(g.Alternatives, []string{"id_column"}) {
		t.Errorf("Alternatives = %v, want id_column: customer_id is identifier-shaped", g.Alternatives)
	}
	// The entity type is nowhere in the table either.
	et, ok := guessFor(res, "orders.csv", "Order")
	if !ok {
		t.Fatalf("no guess for the entity type; guesses = %+v", res.Guesses)
	}
	if !hasAll(et.Alternatives, []string{"Orders"}) {
		t.Errorf("Alternatives = %v, want what the file name would have said", et.Alternatives)
	}
}
