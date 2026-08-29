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
