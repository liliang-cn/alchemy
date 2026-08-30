package neo4j

import (
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func sampleProvenance() alchemy.Provenance {
	return alchemy.Provenance{
		Source:     "architecture.pdf",
		Chunk:      14,
		Producer:   alchemy.ProducerLLMExtract,
		Model:      "gemini-3.6-flash-high",
		Ontology:   "sds@3",
		Chunking:   "heading",
		Confidence: 0.82,
		RuleSet:    "rs-1",
		RuledBy:    "author/type=Vendor",
		ReviewedBy: "ada",
	}
}

// DESIGN.md §5b makes "every entity and relation can name the source, the
// chunk and the producer it came from" a product guarantee, so every field of
// Provenance has to survive the trip. A field silently left behind here is the
// thing the buyer paid for, quietly ending.
func TestProvenanceProps(t *testing.T) {
	got := provenanceProps(sampleProvenance(), "_")
	want := map[string]any{
		"_source":        "architecture.pdf",
		"_chunk":         int64(14),
		"_producer":      "llm-extract",
		"_deterministic": false,
		"_model":         "gemini-3.6-flash-high",
		"_ontology":      "sds@3",
		"_chunking":      "heading",
		"_confidence":    0.82,
		"_rule_set":      "rs-1",
		"_ruled_by":      "author/type=Vendor",
		"_reviewed_by":   "ada",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provenanceProps:\n got %#v\nwant %#v", got, want)
	}
}

// Deterministic is computed rather than left for the buyer to re-derive:
// "filter to the half that was guessed" (§5b) is the query they will write,
// and making them enumerate producer names is handing them a rule the core
// module owns and may extend.
func TestProvenancePropsDeterministic(t *testing.T) {
	p := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	got := provenanceProps(p, "_")
	if got["_deterministic"] != true {
		t.Fatalf("_deterministic = %v for a ddl producer, want true", got["_deterministic"])
	}
	if got["_chunk"] != int64(-1) {
		t.Fatalf("_chunk = %v, want -1 kept: a producer that did not work in chunks says so", got["_chunk"])
	}
	// Empty optional fields are absent rather than empty strings: a property
	// set to "" is one a buyer's WHERE clause has to know to exclude.
	for _, k := range []string{"_model", "_ontology", "_chunking", "_reviewed_by", "_rule_set", "_ruled_by", "_confidence"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s present for a deterministic producer that has none", k)
		}
	}
}

// Neo4j stores primitives and arrays of primitives and nothing else, while an
// Attributes map holds whatever JSON the model produced. A nested value has to
// go somewhere, and where it went has to be legible on the node itself.
func TestAttributeProps(t *testing.T) {
	attrs := map[string]any{
		"founded":   1999.0,
		"public":    true,
		"tags":      []any{"ai", "db"},
		"address":   map[string]any{"city": "Wien"},
		"nothing":   nil,
		"headcount": 12,
	}
	got, encoded, err := attributeProps(attrs, "_")
	if err != nil {
		t.Fatalf("attributeProps: %v", err)
	}
	if got["founded"] != 1999.0 || got["public"] != true || got["headcount"] != int64(12) {
		t.Fatalf("primitives mangled: %#v", got)
	}
	if !reflect.DeepEqual(got["tags"], []any{"ai", "db"}) {
		t.Fatalf("tags = %#v, want the string array intact", got["tags"])
	}
	if got["address"] != `{"city":"Wien"}` {
		t.Fatalf("address = %#v, want JSON text", got["address"])
	}
	if _, ok := got["nothing"]; ok {
		t.Fatalf("a null attribute became a property; Neo4j has no null property")
	}
	if !reflect.DeepEqual(encoded, []string{"address"}) {
		t.Fatalf("encoded = %#v, want the JSON-encoded keys named on the node", encoded)
	}
}
