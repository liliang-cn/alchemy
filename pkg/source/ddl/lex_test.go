package ddl

import (
	"reflect"
	"testing"
)

func texts(toks []token) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		out = append(out, t.text)
	}
	return out
}

func mustLex(t *testing.T, src string) []token {
	t.Helper()
	toks, err := lex(src)
	if err != nil {
		t.Fatalf("lex(%q): %v", src, err)
	}
	return toks
}

func TestLexDropsComments(t *testing.T) {
	toks := mustLex(t, "a -- b\nc /* d\n e */ f # g\nh")
	if got := texts(toks); !reflect.DeepEqual(got, []string{"a", "c", "f", "h"}) {
		t.Errorf("tokens = %v", got)
	}
}

func TestLexUnquotesIdentifiers(t *testing.T) {
	toks := mustLex(t, "`a` \"b\" [c] `d``e` \"f\"\"g\" [h]]i]")
	if got := texts(toks); !reflect.DeepEqual(got, []string{"a", "b", "c", "d`e", `f"g`, "h]i"}) {
		t.Errorf("tokens = %v", got)
	}
	for _, tk := range toks {
		if tk.kind != tokQuoted {
			t.Errorf("%q kind = %v, want tokQuoted", tk.text, tk.kind)
		}
	}
}

// A quoted keyword is an identifier: "TABLE" is a column called TABLE.
func TestQuotedKeywordIsNotAKeyword(t *testing.T) {
	toks := mustLex(t, `"table" table`)
	if toks[0].isWord("table") {
		t.Error(`"table" was read as the keyword TABLE`)
	}
	if !toks[1].isWord("TABLE") {
		t.Error("bare table was not read as the keyword TABLE")
	}
}

func TestLexStringEscapes(t *testing.T) {
	toks := mustLex(t, `'it''s' 'a\'b' 'plain'`)
	if got := texts(toks); !reflect.DeepEqual(got, []string{"it's", "a'b", "plain"}) {
		t.Errorf("tokens = %v", got)
	}
}

func TestLexTracksLines(t *testing.T) {
	toks := mustLex(t, "a\n/* two\nthree */ b\n'four\nfive' c")
	want := []int{1, 3, 4, 5}
	for i, tk := range toks {
		if tk.line != want[i] {
			t.Errorf("token %q line = %d, want %d", tk.text, tk.line, want[i])
		}
	}
}

func TestSplitStatementsIgnoresQuotedSemicolons(t *testing.T) {
	toks := mustLex(t, "a 'x;y'; b `c;d`; e")
	stmts := splitStatements(toks)
	if len(stmts) != 3 {
		t.Fatalf("statements = %d: %v", len(stmts), stmts)
	}
	if got := texts(stmts[1]); !reflect.DeepEqual(got, []string{"b", "c;d"}) {
		t.Errorf("second statement = %v", got)
	}
}

func TestLexRejectsUnterminatedQuotes(t *testing.T) {
	for _, src := range []string{"'abc", "\"abc", "`abc", "[abc", "/* abc"} {
		if _, err := lex(src); err == nil {
			t.Errorf("lex(%q) = nil error, want one", src)
		}
	}
}
