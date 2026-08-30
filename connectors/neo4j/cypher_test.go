package neo4j

import (
	"strings"
	"testing"
)

// The only strings this connector ever concatenates into Cypher are ontology
// types, which come out of a model. So the escaping is tested before anything
// else exists: a connector that builds Cypher by pasting a model's output is a
// remote code execution wearing a graph.
func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Person", "`Person`"},
		{"space", "Legal Entity", "`Legal Entity`"},
		{"backtick doubled", "Foo`Bar", "`Foo``Bar`"},
		{"cypher comment", "Foo`) DETACH DELETE n //", "`Foo``) DETACH DELETE n //`"},
		{"newline", "Foo\nMATCH (n) DELETE n", "`Foo\nMATCH (n) DELETE n`"},
		{"colon", "ns:Person", "`ns:Person`"},
		{"already backticked", "`Person`", "```Person```"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := quoteIdent(c.in)
			if err != nil {
				t.Fatalf("quoteIdent(%q) errored: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("quoteIdent(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// An identifier that cannot round-trip through backticks is refused rather
// than mangled. Silently dropping a byte would give a buyer a label that is
// not the ontology type they declared, which is a lie the ontology cannot
// catch.
func TestQuoteIdentRefuses(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"nul byte", "Per\x00son"},
		{"invalid utf8", "Per\xffson"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := quoteIdent(c.in); err == nil {
				t.Fatalf("quoteIdent(%q) accepted an identifier it cannot represent", c.in)
			}
		})
	}
}

// Every quoted identifier must be balanced: an odd number of unescaped
// backticks is how an injected label would escape its quoting.
func TestQuoteIdentAlwaysBalanced(t *testing.T) {
	for _, in := range []string{"a`", "``", "`", "a``b", "```"} {
		got, err := quoteIdent(in)
		if err != nil {
			t.Fatalf("quoteIdent(%q): %v", in, err)
		}
		if !strings.HasPrefix(got, "`") || !strings.HasSuffix(got, "`") {
			t.Fatalf("quoteIdent(%q) = %q, not backtick-delimited", in, got)
		}
		inner := got[1 : len(got)-1]
		if strings.Count(inner, "`")%2 != 0 {
			t.Fatalf("quoteIdent(%q) = %q leaves an unpaired backtick inside the quotes", in, got)
		}
	}
}
