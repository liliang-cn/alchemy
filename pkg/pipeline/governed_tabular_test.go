package pipeline

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// This file is the customer scenario that found the wiring gap in §5b's third
// mechanism: a governed job with a CSV source produced a graph that was 100%
// violations by construction, because the tabular reader inferred its column
// mapping with a model that was never shown the ontology. The verifier then
// judged that mapping against a list the model had never seen, so every record
// it produced was an unknown type and every edge it minted was dangling.
//
// §5b states the guarantee the document path already keeps: "the extractor is
// constrained by it, and the verifier checks the output against it — the same
// list on both sides of the model". A table is read by a model too, so the
// table gets the same list.

// ravelOps is the vocabulary the failing job declared. The names are the ones
// from the report — Node, StoragePool, Resource, Backend, Site and HOSTS,
// BACKED_BY, PLACED_ON, AT_SITE — and Backend is declared and never produced by
// this table on purpose: an ontology is a closed list of what is allowed, not a
// checklist of what must appear.
const ravelOps = `{"id":"freight-ops@5","parts":{"tabular":{
  "entities":[
    {"name":"Node","description":"One machine in the cluster.","attributes":["capacity_gib","replicas"]},
    {"name":"StoragePool","description":"Backing storage a node offers."},
    {"name":"Resource","description":"A replicated volume placed on nodes."},
    {"name":"Backend","description":"The storage driver behind a pool."},
    {"name":"Site","description":"Where a node physically sits."}],
  "relations":[
    {"name":"HOSTS","from":["Node"],"to":["StoragePool","Resource"],"description":"A node hosts the pools and resources on it."},
    {"name":"BACKED_BY","from":["Node","StoragePool"],"to":["Backend"],"description":"Storage is backed by a driver."},
    {"name":"PLACED_ON","from":["Resource"],"to":["StoragePool"],"description":"A resource is placed on a pool."},
    {"name":"AT_SITE","to":["Site"],"description":"Whatever a site holds."}]}}}`

// ungovernedMapping is what the model answered in the failing run: it was asked
// what a row of inventory.csv is, with nothing to answer from but the file name.
// "a row of inventory.csv was called a Inventory; the table never says what a
// row is" was its own guess, quoted in the report.
const ungovernedMapping = `{"entity_type":"Inventory","id_column":"id","name_column":"",
  "attributes":{"pool_driver":"pool_driver","capacity_gib":"capacity_gib","replicas":"replicas"},
  "relations":[
    {"column":"node_name","relation_type":"HOSTED_ON","target_type":"Node"},
    {"column":"storage_pool_id","relation_type":"IN_STORAGE_POOL","target_type":"StoragePool"},
    {"column":"resource_id","relation_type":"FOR_RESOURCE","target_type":"Resource"},
    {"column":"site","relation_type":"AT_SITE","target_type":"Site"}],
  "confidence":0.5,
  "reasons":{"entity_type":"a row of inventory.csv was called a Inventory; the table never says what a row is"}}`

// governedMapping is the same table mapped by a model that was shown the same
// list the verifier holds. Nothing about the table changed; only what the model
// was told.
const governedMapping = `{"entity_type":"Node","id_column":"id","name_column":"node_name",
  "attributes":{"capacity_gib":"capacity_gib","replicas":"replicas"},
  "relations":[
    {"column":"storage_pool_id","relation_type":"HOSTS","target_type":"StoragePool"},
    {"column":"resource_id","relation_type":"HOSTS","target_type":"Resource"},
    {"column":"pool_driver","relation_type":"BACKED_BY","target_type":"Backend"},
    {"column":"site","relation_type":"AT_SITE","target_type":"Site"}],
  "confidence":0.6,
  "reasons":{"id_column":"id identifies the row itself; storage_pool_id and resource_id identify other things"}}`

// vocabularyAwareLLM answers from what it was shown, which is the only way a
// fake can say anything true about a prompt. Shown the vocabulary it answers in
// the vocabulary's types; shown nothing it invents a shape from the file name,
// exactly as the deployed service did. A fake that always returned the right
// answer would pass whether or not the ontology ever reached the model, which
// is the bug.
type vocabularyAwareLLM struct {
	mu     sync.Mutex
	system string
}

func (l *vocabularyAwareLLM) Name() string { return "mapper-1" }

func (l *vocabularyAwareLLM) Complete(_ context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	l.mu.Lock()
	l.system = req.System
	l.mu.Unlock()
	// "StoragePool" is a type only the ontology knows. The header does not
	// contain it and neither does the reader's own prompt text.
	if strings.Contains(req.System, "StoragePool") {
		return alchemy.LLMResponse{Text: governedMapping}, nil
	}
	return alchemy.LLMResponse{Text: ungovernedMapping}, nil
}

func (l *vocabularyAwareLLM) shown() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.system
}

func inventorySource(t *testing.T) Source {
	t.Helper()
	body, err := os.ReadFile("testdata/inventory.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return Source{Name: "inventory.csv", Kind: alchemy.SourceTabular, Open: openBytes(body)}
}

func loadOntology(t *testing.T, doc string) *ontology.Ontology {
	t.Helper()
	o, err := ontology.Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ontology.Load: %v", err)
	}
	return o
}

// THE ACCEPTANCE TEST.
//
// A six-row CSV, a declared vocabulary, and verification against that same
// vocabulary: zero unknown-type violations and zero dangling edges. Before the
// fix this run produced 42 violations out of 6 entities and 18 relations — a
// graph that is 100% violations by construction, which §5's counts block
// reports as a catastrophe when it is really a wiring gap.
func TestAGovernedTableIsMappedOntoTheDeclaredVocabulary(t *testing.T) {
	llm := &vocabularyAwareLLM{}
	res, err := Run(context.Background(), Request{
		Sources:  []Source{inventorySource(t)},
		Ontology: loadOntology(t, ravelOps),
		Part:     ontology.PartTabular,
		Models:   alchemy.Models{LLM: llm},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(llm.shown(), "Use ONLY these entity types") {
		t.Errorf("the mapping model was never shown the vocabulary; §5b's \"same list on "+
			"both sides of the model\" holds on one side only. System prompt:\n%s", llm.shown())
	}

	byKind := map[alchemy.ViolationKind][]alchemy.Violation{}
	for _, v := range res.Violations {
		byKind[v.Kind] = append(byKind[v.Kind], v)
	}
	for _, kind := range []alchemy.ViolationKind{
		alchemy.ViolationUnknownEntityType,
		alchemy.ViolationUnknownRelationType,
		alchemy.ViolationDanglingRelation,
		alchemy.ViolationRelationNotAllowed,
	} {
		if got := byKind[kind]; len(got) != 0 {
			t.Errorf("%d %s violations, want none: %+v", len(got), kind, got[0])
		}
	}

	// The guess §2.1 is about survives the constraint. "id" is a substring of
	// "storage_pool_id" and of "resource_id", both readings run cleanly, and
	// the only thing that tells them apart is the guess naming what it was
	// chosen over. A vocabulary answers "what is a row", never "which column
	// identifies it", so constraining the first must not silence the second.
	var idGuess alchemy.Guess
	for _, g := range res.Guesses {
		if g.ChosenAs == "id_column" {
			idGuess = g
		}
	}
	if idGuess.Field != "id" {
		t.Fatalf("id_column guess = %+v, want the chosen column reported; guesses = %+v", idGuess, res.Guesses)
	}
	for _, want := range []string{"storage_pool_id", "resource_id"} {
		if !containsString(idGuess.Alternatives, want) {
			t.Errorf("id_column alternatives = %v, want %q among them: a guess that names no "+
				"alternative tells a reviewer nothing", idGuess.Alternatives, want)
		}
	}

	// Every row is one Node and every edge lands on something the result holds.
	if got := len(res.Entities); got != 20 {
		t.Errorf("entities = %d, want 6 rows plus the 14 distinct things their columns name", got)
	}
	if got := len(res.Relations); got != 24 {
		t.Errorf("relations = %d, want four per row", got)
	}
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}
