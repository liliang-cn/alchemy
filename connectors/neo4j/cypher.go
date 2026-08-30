package neo4j

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// quoteIdent renders one label or relationship type as a Cypher identifier.
//
// It exists because Cypher has no way to parameterise a label or a
// relationship type — `MERGE (n:$type)` is a syntax error — while an
// Entity.Type is whatever a model wrote inside an ontology's vocabulary. That
// leaves exactly one place in this package where a string that came from a
// model is concatenated into a query, so that place is a single function with
// a single rule, tested against the strings an attacker would send.
//
// The rule is Cypher's own: a backtick-quoted identifier may contain anything
// except an unpaired backtick, and a backtick inside one is written twice.
// Doubling is used rather than stripping because a stripped label is no longer
// the ontology type the buyer declared, and nothing downstream could tell that
// it had changed — the ontology check has already happened by the time a
// result reaches a connector, so a connector that quietly rewrites a type is
// the one liar the verification chain cannot catch.
func quoteIdent(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty identifier: a type with no name cannot be a label")
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("identifier %q is not valid UTF-8", s)
	}
	// A NUL cannot survive the Bolt wire as part of an identifier, and a
	// truncated label is worse than a refused one for the reason above.
	if strings.ContainsRune(s, 0) {
		return "", fmt.Errorf("identifier %q contains a NUL byte", s)
	}
	return "`" + strings.ReplaceAll(s, "`", "``") + "`", nil
}
