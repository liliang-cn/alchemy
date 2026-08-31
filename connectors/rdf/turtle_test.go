package rdf

import (
	"strings"
	"testing"
)

// This connector writes Turtle by hand and owns no parser, so this file is the
// whole of what stands between a document's text and the server's parser. Every
// case below is a string a source can legitimately contain and a Turtle
// document cannot carry unescaped.

func TestALiteralIsEscapedSoASourceCannotEndTheStringItIsIn(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"a quote", `he said "no"`, `"he said \"no\""`},
		{"a backslash", `C:\Users`, `"C:\\Users"`},
		{"a newline", "one\ntwo", `"one\ntwo"`},
		{"a carriage return", "one\rtwo", `"one\rtwo"`},
		{"a tab", "one\ttwo", `"one\ttwo"`},
		// The escape that ends the statement: a source containing a quote and
		// a full stop can close the literal and open a new triple, which is
		// the Turtle spelling of SQL injection.
		{"a whole statement", `x" . <a> <b> <c> . "`, `"x\" . <a> <b> <c> . \""`},
		{"a control character", "bell\a", `"bell\u0007"`},
		{"a non-ASCII character is left alone", "Wien—Österreich", `"Wien—Österreich"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lit(tc.in)
			if got.err != nil {
				t.Fatalf("lit(%q): %v", tc.in, got.err)
			}
			if got.text != tc.want {
				t.Errorf("lit(%q) = %s, want %s", tc.in, got.text, tc.want)
			}
		})
	}
}

// Invalid UTF-8 is refused rather than repaired. A replacement character
// written into a store is a value that came back different from the one that
// went in, with nothing in the graph saying so.
func TestALiteralThatIsNotUTF8IsRefusedRatherThanRepaired(t *testing.T) {
	if got := lit("\xff\xfe"); got.err == nil {
		t.Fatalf("lit accepted invalid UTF-8 and rendered %s", got.text)
	}
}

// An IRI has no escape mechanism inside <>: a space or an angle bracket ends
// it. Every IRI this package builds goes through segment escaping first, so
// this is the check that the escaping was actually applied — a raw string that
// slipped past it is refused rather than sent.
func TestAnIRIThatCouldEndItsOwnBracketsIsRefused(t *testing.T) {
	for _, bad := range []string{
		"http://x/ a b", "http://x/<y>", `http://x/"y"`, "http://x/{y}", "http://x/y|z",
		"http://x/\ny", "http://x/y\x00", "http://x/\\y",
	} {
		if got := iri(bad); got.err == nil {
			t.Errorf("iri(%q) was accepted as %s", bad, got.text)
		}
	}
	if got := iri("http://x/y#z?a=b"); got.err != nil {
		t.Errorf("iri refused an ordinary IRI: %v", got.err)
	}
}

// Segment escaping is what makes an entity ID or an ontology type safe to put
// in an IRI. It is percent-encoding over UTF-8 bytes rather than stripping,
// because a stripped identifier is no longer the one the ontology declared and
// two different types could escape to one IRI.
func TestASegmentEscapesEverythingThatIsNotUnreserved(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"System", "System"},
		{"USES", "USES"},
		{"a b", "a%20b"},
		{"e/1", "e%2F1"},
		{"a#b", "a%23b"},
		{"Ö", "%C3%96"},
		{"", ""},
	} {
		if got := escapeSegment(tc.in); got != tc.want {
			t.Errorf("escapeSegment(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Two different identifiers must never escape to one IRI, because two entities
// under one IRI are one entity and the second silently overwrites the first.
func TestTwoIdentifiersNeverEscapeToOneSegment(t *testing.T) {
	seen := map[string]string{}
	for _, in := range []string{"a b", "a%20b", "a/b", "a%2Fb", "A", "a", "a#b", "a%23b"} {
		out := escapeSegment(in)
		if prev, ok := seen[out]; ok {
			t.Errorf("%q and %q both escape to %q", prev, in, out)
		}
		seen[out] = in
	}
}

// The whole reason this package exists rather than a reification scheme: a
// triple carries no properties, and RDF-star is how the provenance every
// alchemy relation carries reaches the store on the edge itself.
func TestAQuotedTripleRendersAsTheTermThatCanBeAnnotated(t *testing.T) {
	s, p, o := iri("http://x/s"), iri("http://x/p"), iri("http://x/o")
	got := quoted(s, p, o)
	if got.err != nil {
		t.Fatalf("quoted: %v", got.err)
	}
	if want := "<< <http://x/s> <http://x/p> <http://x/o> >>"; got.text != want {
		t.Errorf("quoted = %s, want %s", got.text, want)
	}
}

// A term that could not be built must not become a document. The builder
// collects the first error rather than rendering a statement with a hole in
// it, because a Turtle document with a hole is a parse error attributed to
// this package on the server rather than to the value that caused it.
func TestADocumentCarryingABadTermRefusesToRenderAtAll(t *testing.T) {
	var d doc
	d.triple(iri("http://x/s"), iri("http://x/p"), lit("fine"))
	d.triple(iri("http://x/ bad"), iri("http://x/p"), lit("also fine"))
	if _, err := d.render(); err == nil {
		t.Fatal("a document containing an unrenderable term produced Turtle")
	}
}

// The typed literals, because a chunk index written as a string sorts and
// compares as one: chunk 10 would fall between 1 and 2, which is the same
// mistake neo4j's citeCypher records having made with toString().
func TestNumbersAndBooleansAreTypedRatherThanStringified(t *testing.T) {
	if got := intLit(-1); got.text != "-1" {
		t.Errorf("intLit(-1) = %s", got.text)
	}
	if got := boolLit(false); got.text != "false" {
		t.Errorf("boolLit(false) = %s", got.text)
	}
	// An explicit xsd:double, because Turtle's bare `0.82` is xsd:decimal and a
	// confidence that came back as a decimal is a different term from the one
	// written by a store that used a double. One spelling, everywhere.
	got := floatLit(0.82)
	if !strings.HasPrefix(got.text, `"0.82"^^<`) || !strings.HasSuffix(got.text, `#double>`) {
		t.Errorf("floatLit(0.82) = %s, want a typed xsd:double", got.text)
	}
}

// A document is rendered whole and posted whole, so the triples it contains
// have to be exactly the ones it was given: a builder that dropped a statement
// would write a graph that reports having written more than it holds.
func TestADocumentRendersEveryStatementItWasGiven(t *testing.T) {
	var d doc
	d.triple(iri("http://x/a"), iri("http://x/p"), lit("one"))
	d.preds(iri("http://x/b"), pair{iri("http://x/q"), intLit(2)}, pair{iri("http://x/r"), boolLit(true)})
	out, err := d.render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if n := strings.Count(out, " .\n"); n != 2 {
		t.Errorf("rendered %d statements, want 2:\n%s", n, out)
	}
	for _, want := range []string{`<http://x/a>`, `"one"`, `<http://x/q> 2`, `<http://x/r> true`} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered document is missing %s:\n%s", want, out)
		}
	}
}

// preds with no pairs writes nothing at all. An empty statement — a subject
// and a full stop — is a Turtle parse error, and the case arrives for real:
// every optional provenance field can be absent at once.
func TestASubjectWithNoPredicatesWritesNothing(t *testing.T) {
	var d doc
	d.preds(iri("http://x/a"))
	out, err := d.render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("rendered %q for a subject with no predicates", out)
	}
}
