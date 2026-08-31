package rdf

import (
	"strconv"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// provFields is alchemy.Provenance as RDF, spelled once.
//
// pgvector has this same problem and had it four times: a column list, a
// projection, a row builder and a scan target, none of which the compiler can
// hold to the same length. Provenance gained By and At, three of the four were
// updated, and every write of five hundred entities failed in production while
// every unit test passed — because a []any one element short compiles.
//
// Here the four lists would have been the Turtle predicates, the SPARQL SELECT
// variables, the WHERE clause's OPTIONAL blocks, and the decoder from the
// result bindings. So they are one list, and the writer, the query builder and
// the decoder are all derived from it. A field added to alchemy.Provenance
// still does not break the build — nothing can make that happen for a table of
// closures — but it now breaks exactly one test, in one place, with a message
// that says which half is missing. See provlists_test.go.
//
// Order matters only for legibility: RDF has no positional writing, so a
// transposition here cannot silently write one value into another's place the
// way a COPY row can. What it *can* do is write a value under the wrong
// predicate, which the round-trip test catches for the same reason pgvector's
// does: every field gets a distinct value.
type provField struct {
	// Var is the SPARQL variable this field is projected as, without the ?.
	Var string
	// Pred is the predicate the value is written under.
	Pred string
	// Write renders the value, and reports false for a value that is not
	// written at all.
	//
	// Omitting rather than writing an empty string is the same decision neo4j
	// makes and for the same reason, sharpened by RDF: a triple asserting
	// al:model "" is a claim that this record's model is the empty string,
	// which is not the claim "nobody said". In a store whose whole business is
	// what was asserted, writing a placeholder is asserting something false.
	Write func(alchemy.Provenance) (term, bool)
	// Optional says a reader must not require the triple to be present, which
	// is exactly the fields Write can omit. It is what decides whether the
	// SPARQL pattern wraps this predicate in an OPTIONAL — and getting it
	// wrong in the permissive direction costs nothing while getting it wrong
	// in the strict direction drops every claim that lacks the field, silently,
	// from every walk.
	Optional bool
	// Read puts one result binding back into a Provenance. It is nil for the
	// one predicate that has no field behind it; see al:stated below.
	Read func(*alchemy.Provenance, string)
}

// provFields carries one entry per alchemy.Provenance field, plus al:stated.
//
// al:stated is the computed one, and it is here for the reason pgvector
// materialises prov_deterministic: §5b promises a person "can filter to the
// half that was guessed", and making a buyer enumerate the producer names to
// write that SPARQL hands them a rule the core module owns and is free to
// extend. It is written by calling alchemy.Producer.Deterministic, never by a
// second copy of the rule in a FILTER.
//
// It is deliberately not read back into anything. recall.NewClaim recomputes
// stated-or-inferred from the producer, because the stored value is the answer
// the rule gave on the day of the import and a reader deciding how far to trust
// a sentence today should be told today's answer.
var provFields = []provField{
	{Var: "source", Pred: pSource,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.Source), true },
		Read:  func(p *alchemy.Provenance, s string) { p.Source = s }},
	// Written even at -1, which alchemy defines as "the producer did not work
	// in chunks". That is a fact about the record and not a missing value, and
	// a store that omitted it would leave a reader unable to tell a DDL import
	// from a record whose chunk was lost.
	{Var: "chunk", Pred: pChunk,
		Write: func(p alchemy.Provenance) (term, bool) { return intLit(p.Chunk), true },
		Read:  func(p *alchemy.Provenance, s string) { p.Chunk = atoi(s) }},
	{Var: "producer", Pred: pProducer,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(string(p.Producer)), true },
		Read:  func(p *alchemy.Provenance, s string) { p.Producer = alchemy.Producer(s) }},
	{Var: "stated", Pred: pStated,
		Write: func(p alchemy.Provenance) (term, bool) { return boolLit(p.Producer.Deterministic()), true }},
	{Var: "model", Pred: pModel, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.Model), p.Model != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.Model = s }},
	{Var: "ontology", Pred: pOntology, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.Ontology), p.Ontology != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.Ontology = s }},
	{Var: "chunking", Pred: pChunking, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.Chunking), p.Chunking != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.Chunking = s }},
	{Var: "confidence", Pred: pConfidence, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return floatLit(p.Confidence), p.Confidence != 0 },
		Read:  func(p *alchemy.Provenance, s string) { p.Confidence = atof(s) }},
	{Var: "reviewedBy", Pred: pReviewedBy, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.ReviewedBy), p.ReviewedBy != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.ReviewedBy = s }},
	{Var: "ruleSet", Pred: pRuleSet, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.RuleSet), p.RuleSet != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.RuleSet = s }},
	{Var: "ruledBy", Pred: pRuledBy, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.RuledBy), p.RuledBy != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.RuledBy = s }},
	// The asserter and the date, for alchemy.ProducerHuman. They are the only
	// thing that distinguishes a fact a named person stated from one a file
	// happened to contain, and they are the exact pair that reached three of
	// pgvector's four lists and not the fourth.
	{Var: "by", Pred: pAsserter, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.By), p.By != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.By = s }},
	{Var: "at", Pred: pAssertedAt, Optional: true,
		Write: func(p alchemy.Provenance) (term, bool) { return lit(p.At), p.At != "" },
		Read:  func(p *alchemy.Provenance, s string) { p.At = s }},
}

// provPairs renders one Provenance as predicate/object pairs, ready to hang off
// whatever is being described.
//
// Whatever is being described is the point. For a relation it is a quoted
// triple — << from USES to >> — and for an entity it is the entity's own
// rdf:type statement. One function serves both, which is the RDF form of the
// property neo4j's provenanceProps buys by being flat for nodes and edges
// alike: §5b's promise is that an entity and a relation can *both* name their
// producer, and a design where one is annotated and the other is a separate
// node would give a buyer two query shapes for one guarantee.
func provPairs(p alchemy.Provenance) []pair {
	out := make([]pair, 0, len(provFields))
	for _, f := range provFields {
		t, ok := f.Write(p)
		if !ok {
			continue
		}
		out = append(out, pair{iri(f.Pred), t})
	}
	return out
}

// provPattern renders the provenance half of a SPARQL WHERE clause, matching
// against subj — which is a variable holding a quoted triple in the walk, and a
// variable holding a node everywhere else.
//
// The required fields are matched directly and the rest are OPTIONAL, from the
// same table that decided which ones were written. That agreement is the whole
// point of the table: a field written conditionally and matched unconditionally
// makes every record lacking it vanish from every result, which is a wrong
// answer with no error attached.
func provPattern(subj string) string {
	var b []byte
	for _, f := range provFields {
		clause := subj + " <" + f.Pred + "> ?" + f.Var + " .\n"
		if f.Optional {
			clause = "OPTIONAL { " + subj + " <" + f.Pred + "> ?" + f.Var + " }\n"
		}
		b = append(b, clause...)
	}
	return string(b)
}

// provVars is the projection, in the table's order.
func provVars() string {
	var b []byte
	for _, f := range provFields {
		b = append(b, " ?"+f.Var...)
	}
	return string(b)
}

// provDecode reads one result row back into a Provenance.
//
// A binding that is absent is left alone rather than written as a zero, which
// is the read side of the write side's decision to omit: the record said
// nothing about its model, so the field says nothing about it either.
func provDecode(row map[string]binding) alchemy.Provenance {
	var p alchemy.Provenance
	for _, f := range provFields {
		if f.Read == nil {
			continue
		}
		b, ok := row[f.Var]
		if !ok {
			continue
		}
		f.Read(&p, b.Value)
	}
	return p
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func atof(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
