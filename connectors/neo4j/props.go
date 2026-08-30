package neo4j

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The reserved property names, without their prefix. Everything alchemy knows
// about a record lives under the prefix and everything the source said lives
// at the top level, which is the one rule that makes a collision impossible to
// reach by accident: a model cannot invent an attribute in our namespace
// without the loader refusing it by name (see validate.go).
const (
	keyRun           = "run"
	keyID            = "id"
	keyType          = "type"
	keyEdgeKey       = "key"
	keySource        = "source"
	keyChunk         = "chunk"
	keyProducer      = "producer"
	keyDeterministic = "deterministic"
	keyModel         = "model"
	keyOntology      = "ontology"
	keyChunking      = "chunking"
	keyConfidence    = "confidence"
	keyReviewedBy    = "reviewed_by"
	keyRuleSet       = "rule_set"
	keyRuledBy       = "ruled_by"
	keyBy            = "by"
	keyAt            = "at"
	keyJSONAttrs     = "json_attrs"
)

// provenanceProps flattens a Provenance onto whatever it is describing.
//
// It is flat, and it is flat for both nodes and relationships, because Neo4j
// cannot hang a node off a relationship. A design in which an entity's
// provenance is a `(:Provenance)` node and an edge's provenance is a bag of
// properties would give the buyer two query languages for one guarantee, and
// §5b's promise is that an entity and a relation can *both* name their
// producer. So the shape that an edge can support is the shape a node gets.
//
// Empty optional fields are omitted rather than written as "". A property set
// to the empty string is one every WHERE clause has to know to exclude, and
// "this record has no model" and "this record's model is the empty string" are
// not the same claim.
func provenanceProps(p alchemy.Provenance, prefix string) map[string]any {
	out := map[string]any{
		prefix + keySource: p.Source,
		// Kept even at -1: DESIGN.md defines -1 as "the producer did not work
		// in chunks", which is a fact about the record, not a missing value.
		prefix + keyChunk:    int64(p.Chunk),
		prefix + keyProducer: string(p.Producer),
		// Computed here rather than left to the buyer. §5b's promise is that a
		// person "can filter to the half that was guessed"; making them
		// enumerate the producer names to do it hands them a rule the core
		// module owns and is free to extend.
		prefix + keyDeterministic: p.Producer.Deterministic(),
	}
	for k, v := range map[string]string{
		keyModel:      p.Model,
		keyOntology:   p.Ontology,
		keyChunking:   p.Chunking,
		keyReviewedBy: p.ReviewedBy,
		keyRuleSet:    p.RuleSet,
		keyRuledBy:    p.RuledBy,
		// The asserter and the date, for alchemy.ProducerHuman. They are the
		// only thing that distinguishes a fact a named person stated from one
		// a file happened to contain, and a store that wrote the producer and
		// dropped these would hold a record saying "a person said so" with no
		// way to ask which — the §5b guarantee inverted.
		keyBy: p.By,
		keyAt: p.At,
	} {
		if v != "" {
			out[prefix+k] = v
		}
	}
	if p.Confidence != 0 {
		out[prefix+keyConfidence] = p.Confidence
	}
	return out
}

// attributeProps turns the model's free-form Attributes map into something
// Bolt can carry, and returns the keys it had to change on the way.
//
// Neo4j stores primitives and arrays of primitives; a JSON object or a mixed
// array has nowhere to go. Refusing the load over one nested attribute would
// make a four-hundred-thousand-record import fail on a field nobody queries,
// so a nested value is written as its JSON text instead — and the keys that
// happened to are named on the node itself, in the `json_attrs` property, so
// that a buyer reading `n.address` can tell it is JSON rather than a string
// the source wrote. A conversion nobody can see from the data is the kind of
// quiet rewrite this connector exists not to do.
func attributeProps(attrs map[string]any, prefix string) (map[string]any, []string, error) {
	if len(attrs) == 0 {
		return map[string]any{}, nil, nil
	}
	out := make(map[string]any, len(attrs))
	var encoded []string
	for k, v := range attrs {
		val, wasEncoded, err := boltValue(v)
		if err != nil {
			return nil, nil, fmt.Errorf("attribute %q: %w", k, err)
		}
		if val == nil {
			// Neo4j has no null property: setting one removes the property.
			// Dropping it is the same outcome, stated in one place.
			continue
		}
		out[k] = val
		if wasEncoded {
			encoded = append(encoded, k)
		}
	}
	// Sorted so that two loads of the same record produce the same property
	// value, which is what makes a replay a no-op rather than a rewrite.
	sort.Strings(encoded)
	return out, encoded, nil
}

// boltValue maps one JSON value onto something the driver can send. The second
// result says whether the value had to be re-encoded as JSON text, which the
// caller records on the record so the change is visible in the graph.
func boltValue(v any) (any, bool, error) {
	switch t := v.(type) {
	case nil:
		return nil, false, nil
	case string, bool, float64, float32, int64, int32, int, []byte:
		return normalizeInt(t), false, nil
	case []any:
		if arr, ok := primitiveArray(t); ok {
			return arr, false, nil
		}
	}
	// Everything else — objects, mixed arrays, arrays of arrays — becomes its
	// JSON text. json.Marshal is deterministic for maps (Go sorts object
	// keys), which matters: a value that re-encoded differently on every load
	// would turn an idempotent replay into a write.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false, fmt.Errorf("value of type %T cannot be stored or encoded: %w", v, err)
	}
	return string(b), true, nil
}

// normalizeInt widens Go's integer types to int64, which is the only integer
// Bolt has. Left alone, a value that arrived as int and one that arrived as
// int64 would compare unequal on read-back and make a replay look like a
// change.
func normalizeInt(v any) any {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int32:
		return int64(t)
	default:
		return v
	}
}

// primitiveArray reports whether a decoded JSON array is uniform enough for
// Neo4j, which stores arrays of one primitive type and nothing else.
func primitiveArray(in []any) ([]any, bool) {
	if len(in) == 0 {
		return in, true
	}
	var kind string
	for _, v := range in {
		var k string
		switch v.(type) {
		case string:
			k = "string"
		case bool:
			k = "bool"
		case float64, float32, int, int32, int64:
			k = "number"
		default:
			return nil, false
		}
		if kind == "" {
			kind = k
		} else if kind != k {
			return nil, false
		}
	}
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = normalizeInt(v)
	}
	return out, true
}
