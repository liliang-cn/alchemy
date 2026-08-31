package rdf

import (
	"reflect"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// pgvector holds four provenance lists to the shape of alchemy.Provenance,
// because four is what it has. This package has one — provFields — and these
// tests are what makes that claim true rather than aspirational: the writer,
// the SPARQL projection, the WHERE pattern and the decoder are all derived from
// it, so the only way a field can go missing is by not being in the table.
//
// A table of closures cannot break the build when alchemy.Provenance grows.
// This is the test that does.
func TestTheProvenanceTableCoversEveryFieldOfAProvenance(t *testing.T) {
	fields := reflect.TypeOf(alchemy.Provenance{}).NumField()
	// One more than the fields, for al:stated, which is computed from the
	// producer and has no field behind it — the same +1 pgvector carries for
	// prov_deterministic. If the difference stops being exactly one, either a
	// field is missing from the table or a predicate has appeared that nothing
	// in Provenance accounts for.
	if got, want := len(provFields), fields+1; got != want {
		t.Fatalf("provFields has %d entries and alchemy.Provenance has %d fields (+1 computed), want %d: "+
			"a field missing from this table is written by nothing, matched by nothing and read by nothing, "+
			"and every test that does not look at the field still passes", got, fields, want)
	}
	computed := 0
	for _, f := range provFields {
		if f.Read == nil {
			computed++
		}
	}
	if computed != 1 {
		t.Errorf("%d entries have no Read; exactly one should — al:stated, which recall.NewClaim recomputes", computed)
	}
}

// Two entries with one predicate would write the second over the first: RDF is
// a set of triples, so two values under one predicate on one subject are two
// values of that predicate, and a decoder taking whichever binding came back
// would report one field's value in another's place.
func TestNoTwoProvenanceFieldsShareAPredicateOrAVariable(t *testing.T) {
	preds, vars := map[string]string{}, map[string]bool{}
	for _, f := range provFields {
		if prev, ok := preds[f.Pred]; ok {
			t.Errorf("%s and %s are both written under <%s>", prev, f.Var, f.Pred)
		}
		preds[f.Pred] = f.Var
		if vars[f.Var] {
			t.Errorf("two entries project as ?%s, so one overwrites the other in every result row", f.Var)
		}
		vars[f.Var] = true
	}
}

// TestProvenanceSurvivesTheRoundTripFieldForField is the claim this whole
// connector is built on, checked without a server.
//
// RDF-star was chosen over reification because a quoted triple can carry the
// whole provenance directly on the assertion. That is only worth anything if
// the whole provenance actually comes back, so every field gets a distinct
// value and the result is compared field for field: two entries whose Write and
// Read disagree about which field they are for would both be non-empty and only
// their contents would say so.
func TestProvenanceSurvivesTheRoundTripFieldForField(t *testing.T) {
	want := alchemy.Provenance{
		Source: "halcyon-profile.pdf", Chunk: 20, Producer: alchemy.ProducerHuman,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading",
		Confidence: 0.82, ReviewedBy: "ana@example.com", RuleSet: "rs-9f21",
		RuledBy: "authored/violation/type=Flag", By: "liliang", At: "2026-08-30T00:00:00Z",
	}

	// The store, as SPARQL would hand it back: one binding per predicate that
	// was actually written.
	row := map[string]binding{}
	for _, f := range provFields {
		term, ok := f.Write(want)
		if !ok {
			continue
		}
		row[f.Var] = binding{Value: unquote(term.text)}
	}
	if got := provDecode(row); got != want {
		t.Errorf("provenance did not survive the round trip\n got %+v\nwant %+v", got, want)
	}
}

// A field the writer omits must be one the reader does not require, or every
// record without it disappears from every walk — a wrong answer with no error
// attached, which is the failure mode a SPARQL join has and a SQL projection
// does not.
func TestEveryFieldTheWriterCanOmitIsOptionalInTheQuery(t *testing.T) {
	empty := alchemy.Provenance{}
	pattern := provPattern("?a")
	for _, f := range provFields {
		if _, written := f.Write(empty); written {
			continue
		}
		if !f.Optional {
			t.Errorf("%s is omitted from an empty provenance but matched unconditionally", f.Var)
		}
		if !strings.Contains(pattern, "OPTIONAL { ?a <"+f.Pred+">") {
			t.Errorf("%s is omitted by the writer and is not OPTIONAL in the pattern:\n%s", f.Var, pattern)
		}
	}
}

// The projection and the pattern come from one table, so they cannot disagree
// about which variables exist — but a variable named in the SELECT and bound
// nowhere is legal SPARQL that always returns unbound, which would look exactly
// like a record that never carried the field.
func TestEveryProjectedVariableIsBoundByThePattern(t *testing.T) {
	pattern := provPattern("?a")
	for _, v := range strings.Fields(provVars()) {
		if !strings.Contains(pattern, v+" ") && !strings.Contains(pattern, v+" }") {
			t.Errorf("%s is projected and never bound", v)
		}
	}
}

// unquote strips the Turtle rendering back to the lexical value SPARQL results
// carry, so the round-trip test above compares what the store would return
// rather than what the writer emitted. It handles only the forms provFields
// produces, which is the point: a term shape it does not know about is a term
// the table gained without this test being told.
func unquote(s string) string {
	if i := strings.LastIndex(s, `"^^<`); i >= 0 {
		s = s[:i+1]
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		s = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(s)
	}
	return s
}
