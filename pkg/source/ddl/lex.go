package ddl

import (
	"fmt"
	"strings"
)

// The lexer exists because the obvious shortcut — strings.Split(ddl, ";") plus
// strings.Index(upper, "REFERENCES") — is wrong on real dumps in ways that are
// silent: a semicolon inside a DEFAULT string ends a statement that had not
// ended, and a column named "references" or a comment mentioning one produces a
// foreign key nobody wrote. Both failures produce a plausible graph, which is
// the failure mode DESIGN.md exists to prevent, so quoting and comments are
// handled once, here, rather than guessed at in every parsing rule.

type tokenKind int

const (
	tokWord   tokenKind = iota // unquoted identifier, keyword or number
	tokQuoted                  // quoted identifier: "x" `x` [x]
	tokString                  // 'literal'
	tokPunct                   // one of ( ) , . ; = and friends
)

type token struct {
	kind tokenKind
	// text is the token's value with quoting removed and escapes resolved, so
	// `order` and "order" and [order] all compare equal to the same identifier.
	text string
	// line is 1-based and is what an error message names, because "somewhere in
	// your 4000-line dump" is not a useful error.
	line int
}

// isWord reports whether t is the unquoted keyword kw. Quoted identifiers are
// deliberately excluded: "TABLE" is a column called TABLE, not the keyword.
func (t token) isWord(kw string) bool {
	return t.kind == tokWord && strings.EqualFold(t.text, kw)
}

func (t token) isPunct(p string) bool { return t.kind == tokPunct && t.text == p }

// isIdent reports whether t can name a table or column.
func (t token) isIdent() bool { return t.kind == tokWord || t.kind == tokQuoted }

// lex turns DDL text into tokens, dropping comments and whitespace.
//
// It fails rather than guesses on an unterminated string, quoted identifier or
// block comment: after an unterminated quote every following statement boundary
// is fiction, and skipping ahead would silently swallow an unknown number of
// tables. A loud error naming the line is the only honest answer.
func lex(src string) ([]token, error) {
	var out []token
	line := 1
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '#' && lineCommentHash(src, i):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			start := line
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return nil, fmt.Errorf("line %d: unterminated block comment", start)
			}
			line += strings.Count(src[i:i+2+j+2], "\n")
			i += 2 + j + 2
		case c == '\'':
			text, next, nl, err := readQuoted(src, i, '\'', true)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			out = append(out, token{kind: tokString, text: text, line: line})
			line += nl
			i = next
		case c == '"' || c == '`':
			text, next, nl, err := readQuoted(src, i, rune(c), false)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			out = append(out, token{kind: tokQuoted, text: text, line: line})
			line += nl
			i = next
		case c == '[':
			text, next, nl, err := readBracketed(src, i)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			out = append(out, token{kind: tokQuoted, text: text, line: line})
			line += nl
			i = next
		case isWordByte(c):
			j := i
			for j < len(src) && isWordByte(src[j]) {
				j++
			}
			out = append(out, token{kind: tokWord, text: src[i:j], line: line})
			i = j
		default:
			out = append(out, token{kind: tokPunct, text: string(c), line: line})
			i++
		}
	}
	return out, nil
}

// lineCommentHash decides whether a '#' starts a MySQL line comment. It only
// does so at the start of a token, never inside one, because '#' is a legal
// character in some identifiers.
func lineCommentHash(src string, i int) bool {
	return i == 0 || !isWordByte(src[i-1])
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '$' || c >= 0x80
}

// readQuoted reads a '...', "..." or `...` run. The closing quote doubled (”)
// is the standard escape and means a literal quote. When backslash is true a
// backslash escapes the next byte, which is MySQL's rule: mysqldump writes \'
// inside INSERT values constantly, and mis-reading one runs the "string" on
// until the next quote — swallowing whole CREATE TABLE statements. The cost is
// a PostgreSQL literal ending in a backslash under standard_conforming_strings,
// which is rare enough to be the better half of the trade.
func readQuoted(src string, i int, q rune, backslash bool) (text string, next int, newlines int, err error) {
	var b strings.Builder
	qb := byte(q)
	for j := i + 1; j < len(src); j++ {
		switch {
		case backslash && src[j] == '\\' && j+1 < len(src):
			b.WriteByte(src[j+1])
			j++
		case src[j] == qb && j+1 < len(src) && src[j+1] == qb:
			b.WriteByte(qb)
			j++
		case src[j] == qb:
			return b.String(), j + 1, strings.Count(src[i:j], "\n"), nil
		default:
			b.WriteByte(src[j])
		}
	}
	return "", 0, 0, fmt.Errorf("unterminated %c-quoted text", qb)
}

// readBracketed reads a SQL Server [identifier], where ]] is a literal ].
func readBracketed(src string, i int) (text string, next int, newlines int, err error) {
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		switch {
		case src[j] == ']' && j+1 < len(src) && src[j+1] == ']':
			b.WriteByte(']')
			j++
		case src[j] == ']':
			return b.String(), j + 1, strings.Count(src[i:j], "\n"), nil
		default:
			b.WriteByte(src[j])
		}
	}
	return "", 0, 0, fmt.Errorf("unterminated [-quoted identifier")
}

// splitStatements cuts the token stream on top-level semicolons.
func splitStatements(toks []token) [][]token {
	var out [][]token
	depth, start := 0, 0
	for i, t := range toks {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case t.isPunct(";") && depth <= 0:
			if i > start {
				out = append(out, toks[start:i])
			}
			start = i + 1
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}
