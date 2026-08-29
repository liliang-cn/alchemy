package document

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Character-reference decoding for the HTML reader.
//
// An unknown named reference is left exactly as it was written. That is the
// house rule of this package applied to one more place: guessing at "&foo;"
// would put a character in the text that the document never contained, and a
// literal "&foo;" is at least true.

// latin1Names are the HTML named references for U+00A0 through U+00FF, in code
// point order. Listing them positionally is shorter than a map literal and
// makes a missing name obvious.
var latin1Names = strings.Fields(`
nbsp iexcl cent pound curren yen brvbar sect uml copy ordf laquo not shy reg macr
deg plusmn sup2 sup3 acute micro para middot cedil sup1 ordm raquo frac14 frac12 frac34 iquest
Agrave Aacute Acirc Atilde Auml Aring AElig Ccedil Egrave Eacute Ecirc Euml Igrave Iacute Icirc Iuml
ETH Ntilde Ograve Oacute Ocirc Otilde Ouml times Oslash Ugrave Uacute Ucirc Uuml Yacute THORN szlig
agrave aacute acirc atilde auml aring aelig ccedil egrave eacute ecirc euml igrave iacute icirc iuml
eth ntilde ograve oacute ocirc otilde ouml divide oslash ugrave uacute ucirc uuml yacute thorn yuml`)

// namedEntities is the rest: the punctuation and symbols that turn up in prose.
var namedEntities = map[string]rune{
	"amp": '&', "lt": '<', "gt": '>', "quot": '"', "apos": '\'',
	"ndash": '–', "mdash": '—', "horbar": '―', "minus": '−',
	"lsquo": '‘', "rsquo": '’', "sbquo": '‚', "ldquo": '“', "rdquo": '”', "bdquo": '„',
	"hellip": '…', "bull": '•', "middot": '·', "dagger": '†', "Dagger": '‡',
	"prime": '′', "Prime": '″', "permil": '‰', "trade": '™', "euro": '€',
	"larr": '←', "uarr": '↑', "rarr": '→', "darr": '↓', "harr": '↔',
	"ne": '≠', "le": '≤', "ge": '≥', "asymp": '≈', "equiv": '≡',
	"lsaquo": '‹', "rsaquo": '›', "oline": '‾', "frasl": '⁄',
	"ensp": ' ', "emsp": ' ', "thinsp": ' ', "zwnj": '‌', "zwj": '‍',
}

// decodeEntities replaces character references in one text node.
func decodeEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// A reference is short; anything longer is an ampersand in prose.
		end := strings.IndexByte(s[i:], ';')
		if end < 0 || end > 12 {
			b.WriteByte('&')
			i++
			continue
		}
		name := s[i+1 : i+end]
		if r, ok := lookupEntity(name); ok {
			b.WriteRune(r)
			i += end + 1
			continue
		}
		b.WriteByte('&')
		i++
	}
	return b.String()
}

func lookupEntity(name string) (rune, bool) {
	if name == "" {
		return 0, false
	}
	if name[0] == '#' {
		return numericEntity(name[1:])
	}
	if r, ok := namedEntities[name]; ok {
		return r, true
	}
	for i, n := range latin1Names {
		if n == name {
			// nbsp becomes an ordinary space: a non-breaking space is a
			// layout instruction, and downstream a chunker splitting on
			// whitespace should treat it as whitespace.
			if n == "nbsp" {
				return ' ', true
			}
			return rune(0xA0 + i), true
		}
	}
	return 0, false
}

func numericEntity(digits string) (rune, bool) {
	base, body := 10, digits
	if len(body) > 1 && (body[0] == 'x' || body[0] == 'X') {
		base, body = 16, body[1:]
	}
	n, err := strconv.ParseInt(body, base, 32)
	if err != nil || n <= 0 {
		return 0, false
	}
	r := rune(n)
	if !utf8.ValidRune(r) {
		return 0, false
	}
	return r, true
}
