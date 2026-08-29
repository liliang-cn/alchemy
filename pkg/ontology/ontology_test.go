package ontology_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// An ontology with no ID makes every graph extracted under it unfalsifiable:
// alchemy.Provenance.Ontology would be empty, and nobody could say which
// vocabulary the graph was checked against.
func TestLoadRejectsMissingID(t *testing.T) {
	const doc = `{
	  "parts": {
	    "prose": {
	      "entities": [{"name": "Cluster"}],
	      "relations": []
	    }
	  }
	}`
	if _, err := ontology.Load(strings.NewReader(doc)); err == nil {
		t.Fatal("Load accepted an ontology with no id; want an error")
	}
}

// §5b's example provenance reads "sds@3". An unversioned id is only half an
// identity: two graphs extracted before and after a type was added would carry
// the same provenance string while having been checked against different rules.
func TestLoadRequiresVersionInID(t *testing.T) {
	body := `"parts": {"prose": {"entities": [{"name": "Cluster"}], "relations": []}}}`

	if _, err := ontology.Load(strings.NewReader(`{"id": "sds", ` + body)); err == nil {
		t.Fatal("Load accepted the unversioned id \"sds\"; want an error")
	}
	o, err := ontology.Load(strings.NewReader(`{"id": "sds@3", ` + body))
	if err != nil {
		t.Fatalf("Load rejected the versioned id \"sds@3\": %v", err)
	}
	if o.ID != "sds@3" {
		t.Fatalf("ID = %q, want \"sds@3\"", o.ID)
	}
}

// A part that declares nothing is the failure oss-agent's extractor found the
// hard way: "Use ONLY these entity types:" followed by nothing is not a weaker
// constraint, it is a self-contradicting one. It must never reach a prompt.
func TestLoadRejectsEmptyPart(t *testing.T) {
	cases := map[string]string{
		"no parts at all":  `{"id": "sds@3", "parts": {}}`,
		"parts key absent": `{"id": "sds@3"}`,
		"part with no entities": `{"id": "sds@3", "parts": {"prose": {
			"entities": [], "relations": []}}}`,
		"part with entities but the vocabulary object is empty": `{"id": "sds@3", "parts": {"prose": {}}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ontology.Load(strings.NewReader(doc)); err == nil {
				t.Fatalf("Load accepted %s; want an error", name)
			}
		})
	}
}

// A misspelled part name would create a vocabulary nobody ever asks for, and
// the extractor for the part that was meant would find nothing declared.
func TestLoadRejectsUnknownPart(t *testing.T) {
	const doc = `{"id": "sds@3", "parts": {"prosee": {
		"entities": [{"name": "Cluster"}], "relations": []}}}`
	_, err := ontology.Load(strings.NewReader(doc))
	if err == nil {
		t.Fatal("Load accepted the part name \"prosee\"; want an error")
	}
	if !strings.Contains(err.Error(), "prosee") {
		t.Fatalf("error does not name the offending part: %v", err)
	}
}

// Two declarations of one name are two answers to "what is this type", and
// which one wins would be decided by document order.
//
// The case-variant pair is the load-bearing case. Matching is case-insensitive
// (see TestMatchingIsCaseInsensitive), so "Cluster" and "cluster" are one type
// to every checker in this package — declaring both means declaring a type the
// ontology can never tell apart from another, which is a bug in the document
// and not a distinction.
func TestLoadRejectsDuplicateTypeNames(t *testing.T) {
	cases := map[string]string{
		"duplicate entity type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}, {"name": "Cluster"}], "relations": []}}}`,
		"case-variant entity type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}, {"name": "cluster"}], "relations": []}}}`,
		"duplicate relation type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}],
			"relations": [{"name": "CONTAINS"}, {"name": "CONTAINS"}]}}}`,
		"case-variant relation type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}],
			"relations": [{"name": "CONTAINS"}, {"name": "contains"}]}}}`,
		"unnamed entity type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": ""}], "relations": []}}}`,
		"unnamed relation type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}], "relations": [{"description": "x"}]}}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ontology.Load(strings.NewReader(doc)); err == nil {
				t.Fatalf("Load accepted %s; want an error", name)
			}
		})
	}
}

// The same name in two DIFFERENT parts is not a duplicate. A code `contains`
// and a prose `CONTAINS` share a word and nothing else — "a file contains a
// function" and "a cluster contains a node" are different edges — and the
// partition is exactly what keeps them from being conflated.
func TestLoadAllowsOneNameInTwoParts(t *testing.T) {
	const doc = `{"id": "sds@3", "parts": {
		"prose": {"entities": [{"name": "Cluster"}, {"name": "Node"}],
		          "relations": [{"name": "CONTAINS", "from": ["Cluster"], "to": ["Node"]}]},
		"code":  {"entities": [{"name": "file"}, {"name": "function"}],
		          "relations": [{"name": "contains", "from": ["file"], "to": ["function"]}]}}}`
	if _, err := ontology.Load(strings.NewReader(doc)); err != nil {
		t.Fatalf("Load rejected one name declared in two parts: %v", err)
	}
}

// An endpoint naming a type the part does not declare makes the two sides of
// the model disagree: the prompt would offer a relation nothing can legally
// terminate, and verification would reject every edge of that type. That is a
// document bug, and it must cost the load rather than a whole extraction run.
func TestLoadRejectsEndpointNotDeclaredByThePart(t *testing.T) {
	cases := map[string]string{
		"from names an undeclared type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}],
			"relations": [{"name": "CONTAINS", "from": ["Banana"], "to": ["Cluster"]}]}}}`,
		"to names an undeclared type": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}],
			"relations": [{"name": "CONTAINS", "from": ["Cluster"], "to": ["Banana"]}]}}}`,
		// The cross-part case: "file" is a real type, but not in this part.
		// The partition is only structural if it holds here too.
		"endpoint borrowed from another part": `{"id": "sds@3", "parts": {
			"prose": {"entities": [{"name": "Cluster"}],
			          "relations": [{"name": "CONTAINS", "from": ["Cluster"], "to": ["file"]}]},
			"code":  {"entities": [{"name": "file"}], "relations": []}}}`,
		"endpoint is blank": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}],
			"relations": [{"name": "CONTAINS", "from": ["  "], "to": ["Cluster"]}]}}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ontology.Load(strings.NewReader(doc))
			if err == nil {
				t.Fatalf("Load accepted %s; want an error", name)
			}
			if !strings.Contains(err.Error(), "CONTAINS") {
				t.Fatalf("error does not name the relation: %v", err)
			}
		})
	}
}

// An endpoint spelled with different case than the entity type it names is the
// same tolerance the matcher applies, applied at load.
func TestLoadAcceptsCaseVariantEndpoint(t *testing.T) {
	const doc = `{"id": "sds@3", "parts": {"prose": {
		"entities": [{"name": "Cluster"}, {"name": "Node"}],
		"relations": [{"name": "CONTAINS", "from": ["cluster"], "to": ["NODE"]}]}}}`
	if _, err := ontology.Load(strings.NewReader(doc)); err != nil {
		t.Fatalf("Load rejected a case-variant endpoint: %v", err)
	}
}

// The partition is only structural if the parts cannot be reached except by
// name. An exported map is a shared, mutable, cross-part handle: a caller can
// merge two parts into one slice in a single range statement, or assign the
// code vocabulary over the prose one after Load has validated it, and neither
// is a compile error. So the map is unexported and Parts() reports only WHICH
// parts exist — the types come out one part at a time through Vocabulary.
func TestPartsReportsTheDeclaredPartsWithoutTheirTypes(t *testing.T) {
	const doc = `{"id": "sds@3", "parts": {
		"code":  {"entities": [{"name": "file"}], "relations": []},
		"prose": {"entities": [{"name": "Cluster"}], "relations": []}}}`
	o := load(t, doc)

	got := o.Parts()
	want := []ontology.Part{ontology.PartCode, ontology.PartProse}
	if len(got) != len(want) {
		t.Fatalf("Parts() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Parts() = %v, want %v (sorted, so two loads of one document agree)", got, want)
		}
	}
}

// A misspelled key must not load. "entites" or "form" would leave the author
// with a rule they believe is in force and that nothing enforces, and an
// ontology nobody can trust to be what it says is worse than none.
func TestLoadRejectsMalformedAndMisspelledDocuments(t *testing.T) {
	cases := map[string]string{
		"not JSON at all":     `id = "sds@3"`,
		"truncated":           `{"id": "sds@3", "parts": {`,
		"misspelled top key":  `{"id": "sds@3", "part": {"prose": {"entities": [{"name": "Cluster"}]}}}`,
		"misspelled part key": `{"id": "sds@3", "parts": {"prose": {"entites": [{"name": "Cluster"}]}}}`,
		"misspelled endpoint key": `{"id": "sds@3", "parts": {"prose": {
			"entities": [{"name": "Cluster"}, {"name": "Node"}],
			"relations": [{"name": "CONTAINS", "form": ["Cluster"], "to": ["Node"]}]}}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ontology.Load(strings.NewReader(doc)); err == nil {
				t.Fatalf("Load accepted %s; want an error", name)
			}
		})
	}
}
