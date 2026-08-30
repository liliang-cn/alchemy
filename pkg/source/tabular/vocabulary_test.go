package tabular

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// ravelVocab is the vocabulary from the job that found the wiring gap: a
// governed table read by a model that was never shown the list the verifier
// holds, so every row it produced was an unknown type.
var ravelVocab = ontology.Vocabulary{
	Entities: []ontology.EntityType{
		{Name: "Node", Description: "One machine in the cluster."},
		{Name: "StoragePool"},
		{Name: "Resource"},
		{Name: "Site"},
	},
	Relations: []ontology.RelationType{
		{Name: "HOSTS", From: []string{"Node"}, To: []string{"StoragePool", "Resource"}},
		{Name: "AT_SITE", To: []string{"Site"}},
	},
}

// DESIGN.md §5b, third mechanism: "the extractor is constrained by it, and the
// verifier checks the output against it — the same list on both sides of the
// model". A table is read by a model too. Asking it to invent a shape and then
// judging that shape against a list it was never shown is not a check, it is a
// guaranteed violation per row.
func TestTheVocabularyReachesTheMappingPrompt(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"entity_type":"Node","id_column":"id","confidence":0.6}`}
	_, err := Read(context.Background(), "inventory.csv", strings.NewReader("id,site\n1,vienna\n"), Options{
		Delimiter: ',', LLM: llm, Vocabulary: ravelVocab,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(llm.asked.System, "Use ONLY these entity types") {
		t.Fatalf("the mapping prompt does not constrain the model:\n%s", llm.asked.System)
	}
	for _, want := range []string{"Node", "StoragePool", "Resource", "Site", "HOSTS", "AT_SITE"} {
		if !strings.Contains(llm.asked.System, want) {
			t.Errorf("the mapping prompt omits the declared type %q; the verifier will accept "+
				"what the model was never offered:\n%s", want, llm.asked.System)
		}
	}
	// The words are the ontology's, verbatim. A reader that paraphrased the
	// list would be constraining the model against a second ontology nothing
	// checks against the first.
	if !strings.Contains(llm.asked.System, ravelVocab.TablePrompt()) {
		t.Errorf("the vocabulary block is not what pkg/ontology rendered:\n%s", llm.asked.System)
	}
}

// §5 requires an ontology only for document sources, so a tabular job without
// one is legal and its behaviour must not shift by a byte: it infers freely and
// reports every decision as a Guess.
func TestATableWithNoVocabularyIsAskedExactlyWhatItAlwaysWas(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"entity_type":"Order","id_column":"id","confidence":0.6}`}
	res, err := Read(context.Background(), "orders.csv", strings.NewReader("id,total\n1,9\n"), Options{
		Delimiter: ',', LLM: llm,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if llm.asked.System != inferSystem {
		t.Fatalf("an ungoverned table was asked a different question:\n--- got ---\n%s\n--- want ---\n%s",
			llm.asked.System, inferSystem)
	}
	if len(res.Guesses) == 0 {
		t.Error("an inferred mapping reported no guesses")
	}
}

// §5's "there is no unconstrained mode" is about what the model is TOLD, not
// about rewriting what it says. pkg/extract handles a type outside the
// vocabulary by letting the verifier catch it, and inventing a second
// convention here — silently retyping, or refusing the table — would either
// erase the evidence a reviewer needs or throw away five good columns over one
// bad one. So the mapping comes back as the model wrote it and the violation
// is the verifier's to report.
func TestATypeOutsideTheVocabularyIsReturnedRatherThanRewrittenOrRefused(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"entity_type":"Inventory","id_column":"id","confidence":0.5}`}
	res, err := Read(context.Background(), "inventory.csv", strings.NewReader("id,site\n1,vienna\n"), Options{
		Delimiter: ',', LLM: llm, Vocabulary: ravelVocab,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].Type != "Inventory" {
		t.Fatalf("entities = %+v, want the model's own answer kept for the verifier to judge", res.Entities)
	}
}

// EntityHint is a free-text statement of what a row is. Under a vocabulary a
// hint the vocabulary does not declare is not a hint, it is a contradiction:
// the same prompt would carry "use ONLY these types" and "the caller believes a
// row is an Inventory", and two contradicting instructions in one prompt is not
// a constraint, it is a coin flip — §2.1's exact failure mode. So it is refused
// before the call rather than after it, because the answer to a self-
// contradicting prompt is not worth paying for.
func TestAnEntityHintTheVocabularyDoesNotDeclareIsRefusedBeforeTheModelIsCalled(t *testing.T) {
	_, err := Read(context.Background(), "inventory.csv", strings.NewReader("id,site\n1,vienna\n"), Options{
		Delimiter: ',', LLM: &refusingLLM{t: t}, Vocabulary: ravelVocab, EntityHint: "Inventory",
	})
	if err == nil {
		t.Fatal("want a refusal for a hint the vocabulary does not declare")
	}
	if !strings.Contains(err.Error(), "Inventory") || !strings.Contains(err.Error(), "Node") {
		t.Errorf("error = %q, want it to name the hint and the types that were declared", err)
	}
}

// A hint the vocabulary does declare is a statement, and §2.1's first lesson
// says a statement beats an inference. It is canonicalised to the spelling the
// ontology uses, for the reason CanonicalEntity exists: a graph carrying Node
// and node has two node types where the ontology declares one.
func TestAnEntityHintTheVocabularyDeclaresIsKeptInTheOntologysSpelling(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"id_column":"id","confidence":0.9}`}
	res, err := Read(context.Background(), "inventory.csv", strings.NewReader("id,site\n1,vienna\n"), Options{
		Delimiter: ',', LLM: llm, Vocabulary: ravelVocab, EntityHint: "node",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].Type != "Node" {
		t.Fatalf("entities = %+v, want the declared spelling of the caller's hint", res.Entities)
	}
	if !strings.Contains(llm.asked.Prompt, "Node") {
		t.Errorf("the hint the model was shown is not the declared spelling:\n%s", llm.asked.Prompt)
	}
}

// The dangling edge from the report: inventory:1 -[HOSTED_ON]-> node:node-a
// pointed at a node the result did not contain, because the reader minted an
// edge to an entity it never created.
//
// A mapping that names a target type has already decided that the thing exists
// and what its id is — entityID computes it — so withholding the entity is the
// reader asserting an identity it will not stand behind. The cell IS the
// target's identifier as the file states it, which is more than a foreign key
// name is, so the entity is created.
func TestAColumnMappedToATargetTypeCreatesTheEntityItPointsAt(t *testing.T) {
	res, err := readFixed(t, "id,site\n1,vienna\n", &Mapping{
		EntityType: "Node", IDColumn: "id",
		Relations: []RelationMapping{{Column: "site", RelationType: "AT_SITE", TargetType: "Site"}},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %+v, want the row and the site it names", res.Entities)
	}
	site := res.Entities[1]
	if site.ID != "site:vienna" || site.Type != "Site" || site.Name != "vienna" {
		t.Errorf("referenced entity = %+v, want the site the column names", site)
	}
	// It carries the same provenance as the row that named it: a reviewer
	// looking at an entity nothing described still has to be able to find the
	// file and the mapping that minted it.
	if site.Provenance.Source != "t.csv" || site.Provenance.Producer != alchemy.ProducerTabular {
		t.Errorf("referenced entity provenance = %+v", site.Provenance)
	}
	// The row comes first, whatever it names. Order is a property of the file.
	if res.Entities[0].ID != "node:1" {
		t.Errorf("entities = %+v, want the rows before the things they name", res.Entities)
	}
}

// The same node named by twenty rows is one node. Entity.ID is derived from the
// target type and the cell, so the collision is the point rather than a hazard:
// what would be twenty stubs is one, and it is the same id every re-import.
func TestTheSameTargetNamedByEveryRowIsOneEntity(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,node\n")
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "%d,node-a\n", i)
	}
	res, err := readFixed(t, b.String(), &Mapping{
		EntityType: "Pool", IDColumn: "id",
		Relations: []RelationMapping{{Column: "node", RelationType: "ON", TargetType: "Node"}},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 21 {
		t.Fatalf("entities = %d, want twenty rows and the one node they all name", len(res.Entities))
	}
	if len(res.Relations) != 20 {
		t.Fatalf("relations = %d, want one per row", len(res.Relations))
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none: one node named twenty times is not a duplicate id", res.Violations)
	}
}

// A row describing a thing another row merely names must win. The stub carries
// an id and nothing else; the row carries the attributes, and a table that
// happened to mention node-a before describing it would otherwise come back
// with the description missing and a duplicate-id violation in its place.
func TestARowThatDescribesAThingBeatsTheColumnThatOnlyNamesIt(t *testing.T) {
	res, err := readFixed(t, "id,parent,region\nnode-b,node-a,eu\nnode-a,,us\n", &Mapping{
		EntityType: "Node", IDColumn: "id",
		Attributes: map[string]string{"region": "region"},
		Relations:  []RelationMapping{{Column: "parent", RelationType: "CHILD_OF", TargetType: "Node"}},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %+v, want one per row and no stub beside them", res.Entities)
	}
	for _, e := range res.Entities {
		if e.ID == "node:node-a" && e.Attributes["region"] != "us" {
			t.Errorf("node-a = %+v, want the row that describes it rather than the column that names it", e)
		}
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none", res.Violations)
	}
}

// A guess names what else the answer could have been, and under a vocabulary
// what it could have been is the other declared types. Offering "Inventory" and
// "Inventories" from the file name would be offering a reviewer two answers the
// verifier is guaranteed to reject.
func TestUnderAVocabularyTheEntityTypeGuessNamesTheOtherDeclaredTypes(t *testing.T) {
	llm := &fakeLLM{name: "m", reply: `{"entity_type":"Node","id_column":"id","confidence":0.6}`}
	res, err := Read(context.Background(), "inventory.csv", strings.NewReader("id,site\n1,vienna\n"), Options{
		Delimiter: ',', LLM: llm, Vocabulary: ravelVocab,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	g, ok := guessFor(res, "inventory.csv", "Node")
	if !ok {
		t.Fatalf("no entity-type guess; guesses = %+v", res.Guesses)
	}
	if !hasAll(g.Alternatives, []string{"StoragePool", "Resource", "Site"}) {
		t.Errorf("Alternatives = %v, want the other declared types", g.Alternatives)
	}
	for _, never := range g.Alternatives {
		if never == "Inventory" || never == "Inventories" {
			t.Errorf("Alternatives = %v, want no type the vocabulary does not declare", g.Alternatives)
		}
	}
}
