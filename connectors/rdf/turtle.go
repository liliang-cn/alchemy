package rdf

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file is the Turtle writer this connector owns, and owning it is a
// decision rather than an omission.
//
// The alternative was a library, and the module's dependency list is the
// checkable form of DESIGN.md §4's argument that a buyer who wants one store
// does not pay for four. What a writer needs is small and completely specified:
// three term forms, one statement form, and the escaping rules the Turtle
// grammar states. What a *parser* needs is neither — and this package needs no
// parser, because everything it reads back comes as SPARQL results in JSON,
// which encoding/json already understands.
//
// So this is the one place in the package where a string that came out of a
// customer's document is concatenated into something a server will parse. It is
// one function per term kind, with one rule each, tested against the strings an
// attacker would send — the same shape, and for the same reason, as neo4j's
// quoteIdent.

// term is one rendered RDF term, or the reason it could not be rendered.
//
// The error travels with the value rather than being returned beside it because
// a term is built inside an expression — quoted(iri(a), iri(b), iri(c)) — and a
// writer that had to check three errors per statement would check none of them
// by the fifth call site. doc.render is where the first one surfaces, and it
// surfaces as a refusal to produce a document at all.
type term struct {
	text string
	err  error
}

// pair is one predicate and its object, for a subject that carries several.
type pair struct{ p, o term }

// iri renders an absolute IRI.
//
// Turtle's IRIREF has no escape mechanism worth using: the characters below
// simply end the reference or are outright illegal inside it, so an IRI
// containing one cannot be written, only refused. Every IRI this package builds
// is assembled from a base and escapeSegment'd parts, which cannot produce any
// of them — so reaching this refusal means the assembly was bypassed, and the
// right answer is to fail loudly rather than to emit a document the server will
// reject with a parse error attributed to us.
func iri(s string) term {
	if s == "" {
		return term{err: fmt.Errorf("rdf: an empty IRI names nothing")}
	}
	if !utf8.ValidString(s) {
		return term{err: fmt.Errorf("rdf: IRI %q is not valid UTF-8", s)}
	}
	for _, r := range s {
		if r <= 0x20 || strings.ContainsRune("<>\"{}|^`\\", r) {
			return term{err: fmt.Errorf("rdf: IRI %q contains %q, which cannot appear inside <>", s, r)}
		}
	}
	return term{text: "<" + s + ">"}
}

// lit renders a plain string literal.
//
// The escapes are Turtle's own. The one that matters is the quote: a document
// that says `he said "no". <a> <b> <c>` would otherwise close the literal and
// open a statement, which is this format's spelling of an injection — and the
// text being written here is, by construction, whatever a customer's PDF
// happened to contain.
//
// Control characters below 0x20 that have no short escape are written as \u
// rather than dropped. A store that silently removed a byte would hold text
// that no longer matches the byte offsets Citation.Start and Citation.End
// promise a reader they can open the file with.
func lit(s string) term {
	if !utf8.ValidString(s) {
		return term{err: fmt.Errorf("rdf: literal is not valid UTF-8: %q", s)}
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return term{text: b.String()}
}

// The XSD datatypes this package writes. They are spelled as full IRIs rather
// than through a @prefix because every document this writer produces is posted
// on its own and a prefix declared in one batch is not in scope in the next —
// which is the kind of dependency between two HTTP requests that works until a
// retry sends them out of order.
const (
	xsdInteger = "http://www.w3.org/2001/XMLSchema#integer"
	xsdDouble  = "http://www.w3.org/2001/XMLSchema#double"
	xsdBoolean = "http://www.w3.org/2001/XMLSchema#boolean"
)

// intLit renders an integer.
//
// Typed rather than written as a string, because a chunk index is compared and
// ordered: as text, chunk 10 falls between 1 and 2. neo4j's citeCypher records
// having made exactly that mistake with toString(), on the same field.
func intLit(n int) term {
	return term{text: strconv.Itoa(n)}
}

// floatLit renders a confidence.
//
// Explicitly xsd:double, because Turtle's bare `0.82` is an xsd:decimal and
// `8.2e-1` is a double: the same number reaches the store as two different
// terms depending on how it was spelled, and a query filtering on one finds
// nothing written by the other. One spelling here means one term in the store.
func floatLit(f float64) term {
	return term{text: `"` + strconv.FormatFloat(f, 'g', -1, 64) + `"^^<` + xsdDouble + ">"}
}

func boolLit(b bool) term {
	return term{text: strconv.FormatBool(b)}
}

// quoted renders << s p o >>, the RDF-star term that names a triple so that
// something can be said about it.
//
// It is the whole reason this connector exists in the shape it does. See the
// package doc: an RDF triple cannot carry properties, every alchemy relation
// carries a provenance, and this is the term that lets the provenance be
// attached to the assertion rather than to one of its ends.
func quoted(s, p, o term) term {
	if err := firstErr(s, p, o); err != nil {
		return term{err: err}
	}
	return term{text: "<< " + s.text + " " + p.text + " " + o.text + " >>"}
}

func firstErr(terms ...term) error {
	for _, t := range terms {
		if t.err != nil {
			return t.err
		}
	}
	return nil
}

// doc accumulates statements and the first thing that went wrong.
//
// All-or-nothing is the point. A document is posted in one request, so a
// builder that skipped an unrenderable statement would write a graph holding
// fewer records than the report says it wrote, and one that emitted the
// statement anyway would hand the server a parse error naming a line number in
// a document the caller never sees.
type doc struct {
	b   strings.Builder
	err error
}

// triple writes one statement.
func (d *doc) triple(s, p, o term) {
	d.preds(s, pair{p, o})
}

// preds writes one subject with several predicates, as `s p1 o1 ; p2 o2 .`.
//
// No pairs writes nothing at all, and that case is ordinary rather than
// defensive: every optional provenance field can be absent at once, and a
// subject followed by a full stop is a Turtle parse error.
func (d *doc) preds(s term, pairs ...pair) {
	if len(pairs) == 0 {
		return
	}
	if d.err != nil {
		return
	}
	if s.err != nil {
		d.err = s.err
		return
	}
	for _, p := range pairs {
		if err := firstErr(p.p, p.o); err != nil {
			d.err = err
			return
		}
	}
	d.b.WriteString(s.text)
	for i, p := range pairs {
		if i > 0 {
			d.b.WriteString(" ;")
		}
		d.b.WriteString(" " + p.p.text + " " + p.o.text)
	}
	d.b.WriteString(" .\n")
}

// empty reports whether anything was written, so a caller can skip a request
// rather than posting a document with no statements in it.
func (d *doc) empty() bool { return d.b.Len() == 0 }

func (d *doc) render() (string, error) {
	if d.err != nil {
		return "", d.err
	}
	return d.b.String(), nil
}

// escapeSegment percent-encodes one identifier for use in an IRI path.
//
// The unreserved set of RFC 3986 is kept and everything else is encoded over
// its UTF-8 bytes. Two properties are being bought, and the second is the one
// that matters: an ordinary ontology type stays readable in the store, where a
// buyer browsing the repository sees <.../type/System> rather than a hash; and
// the mapping is injective, so two entity IDs can never land on one IRI. That
// second is not a nicety — two entities under one IRI are one entity in RDF,
// and the second would silently take the first's place with the graph reporting
// two.
//
// Stripping was the alternative and is what neo4j's quoteIdent declines for the
// same reason: an identifier with characters removed is no longer the one the
// producer wrote, and nothing downstream could tell that it had changed.
func escapeSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
