package cortexdb

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The two provenance models, and where they meet.
//
// CortexDB already knows where a fact came from. FactProvenance reads four
// things off an edge — document_id, chunk_ids, inferred + rule_id — and
// `Cited()` calls a fact accounted for when it has any of them. Alchemy's
// Provenance carries ten. Three of the four line up exactly and are written
// through CortexDB's own fields, because a connector that put them somewhere
// private would leave `fact_provenance` answering "uncited" about a graph whose
// every edge names its source:
//
//	alchemy Source   -> CortexDB document_id   (via documentID, in plan.go)
//	alchemy Chunk    -> CortexDB chunk_ids     (the chunk this run wrote)
//	alchemy Producer -> CortexDB inferred      (Producer.Deterministic, negated)
//
// The fourth does not. CortexDB's rule_id means "this fact was *derived* by
// that rule, and the chunks below support the premises, not the conclusion".
// Alchemy's RuledBy means "a standing policy retyped or renamed this record" —
// the record was still read from the source, not derived from other facts.
// Writing RuledBy into rule_id would make every policy-touched edge read as an
// inference with no evidence of its own, which is a worse lie than having
// nowhere to put it. So it goes in metadata and is named in the report.
//
// Six fields have nowhere native at all: Model, Ontology, Chunking, Confidence,
// ReviewedBy, RuleSet. They are written as reserved-prefix metadata — CortexDB
// stores node and edge properties as free JSON, so nothing is lost, but nothing
// in CortexDB's own vocabulary asks for them either. What that costs is stated
// in the connector's report rather than discovered.
//
// Confidence is deliberately *not* mapped onto GraphEdge.Weight even though
// both are a float on an edge. Weight is what CortexDB's traversal ranks by;
// confidence is what a model said about itself. An edge a model was unsure of
// is not an edge a reader should reach less often — it is one they should reach
// and distrust — and quietly turning one into the other would change what every
// GraphRAG answer is ranked by without anybody having asked.
const (
	keyRun           = "run"
	keyEntityID      = "id"
	keyDeclaredType  = "declared_type"
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
	keyAssertions    = "assertions"
	keyProvenance    = "provenance"
)

// provenanceMeta flattens one Provenance into CortexDB's string-valued
// metadata.
//
// Empty optional fields are omitted rather than written as "": "this record has
// no model" and "this record's model is the empty string" are not the same
// claim, and a property set to "" is one every filter has to know to exclude.
// Chunk is kept even at -1, because DESIGN.md defines -1 as "the producer did
// not work in chunks", which is a fact about the record rather than a missing
// value.
func provenanceMeta(p alchemy.Provenance, prefix string) map[string]string {
	out := map[string]string{
		prefix + keySource:   p.Source,
		prefix + keyChunk:    strconv.Itoa(p.Chunk),
		prefix + keyProducer: string(p.Producer),
		// Computed here rather than left to the buyer. §5b's promise is that a
		// person "can filter to the half that was guessed"; making them
		// enumerate the producer names hands them a rule the core module owns
		// and is free to extend.
		prefix + keyDeterministic: strconv.FormatBool(p.Producer.Deterministic()),
	}
	for k, v := range map[string]string{
		keyModel:      p.Model,
		keyOntology:   p.Ontology,
		keyChunking:   p.Chunking,
		keyReviewedBy: p.ReviewedBy,
		keyRuleSet:    p.RuleSet,
		keyRuledBy:    p.RuledBy,
		// The asserter and the date, for alchemy.ProducerHuman. Without them a
		// human assertion in this store says "a person said so" and cannot say
		// which person, which is the one thing that made it admissible.
		keyBy: p.By,
		keyAt: p.At,
	} {
		if v != "" {
			out[prefix+k] = v
		}
	}
	if p.Confidence != 0 {
		out[prefix+keyConfidence] = strconv.FormatFloat(p.Confidence, 'g', -1, 64)
	}
	return out
}

// renderProvenance writes one line for CortexDB's own free-text `provenance`
// property, which is what the fact_provenance tool shows a person asking "says
// who?". It is a rendering, not the record: the fields are in metadata too.
func renderProvenance(p alchemy.Provenance) string {
	var b strings.Builder
	b.WriteString("alchemy: ")
	b.WriteString(string(p.Producer))
	if p.Model != "" {
		b.WriteString(" via " + p.Model)
	}
	b.WriteString(", " + p.Source)
	if p.Chunk >= 0 {
		fmt.Fprintf(&b, " chunk %d", p.Chunk)
	}
	if p.Ontology != "" {
		b.WriteString(", ontology " + p.Ontology)
	}
	if p.ReviewedBy != "" {
		b.WriteString(", reviewed by " + p.ReviewedBy)
	}
	if p.RuledBy != "" {
		b.WriteString(", ruled by " + p.RuledBy)
	}
	return b.String()
}

// attributeMeta turns the source's free-form Attributes into CortexDB's
// string-valued metadata, and names the keys it had to re-encode on the way.
//
// CortexDB's tool inputs take map[string]string, so a number, a boolean or a
// nested object has nowhere to go as itself. Refusing the load over one such
// attribute would fail a four-hundred-thousand-record import on a field nobody
// queries, so the value is written as its JSON text instead — and the keys that
// happened to are listed under the reserved `json_attrs`, so a reader seeing
// "2024" can tell whether the source said a number or the string. A conversion
// nobody can see from the data is the quiet rewrite this connector exists not
// to do.
func attributeMeta(attrs map[string]any, prefix string, into map[string]string) error {
	var encoded []string
	for k, v := range attrs {
		if s, ok := v.(string); ok {
			into[k] = s
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("attribute %q of type %T cannot be stored or encoded: %w", k, v, err)
		}
		into[k] = string(b)
		encoded = append(encoded, k)
	}
	if len(encoded) > 0 {
		// Sorted so two loads of one record produce the same value, which is
		// what makes a replay a no-op rather than a rewrite.
		sort.Strings(encoded)
		into[prefix+keyJSONAttrs] = strings.Join(encoded, ",")
	}
	return nil
}
