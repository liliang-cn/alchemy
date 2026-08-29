package extract

import (
	"encoding/json"
	"fmt"
	"strings"
)

// reply is the shape the extractor asks the model for. It is deliberately not
// alchemy.Entity: what a model returns is a proposal with no ID and no
// provenance, and giving it the finished type would let an unowned, unsourced
// entity travel one function further than it should.
type reply struct {
	Entities  []rawEntity   `json:"entities"`
	Relations []rawRelation `json:"relations"`
}

type rawEntity struct {
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
	// Confidence is the model's own, and absent means absent: 0 is what a
	// missing field decodes to and is also a real answer, but a model that
	// omits the field has said nothing rather than said zero. The distinction
	// costs nothing here because 0 lands in Provenance.Confidence either way.
	Confidence float64 `json:"confidence"`
}

type rawRelation struct {
	Type string `json:"type"`
	// From and To are held raw because models write an end either as a bare
	// name ("SuperAI") or as an object ({"name":"SuperAI","type":"Cluster"}),
	// and a decoder that accepts only one of them turns the other into a
	// parse failure for a reply that was perfectly clear.
	From     json.RawMessage `json:"from"`
	To       json.RawMessage `json:"to"`
	FromType string          `json:"from_type"`
	ToType   string          `json:"to_type"`

	Attributes map[string]any `json:"attributes"`
	Confidence float64        `json:"confidence"`
}

// end is one resolved end of a relation: a name, and a type when the model
// gave one.
type end struct {
	Name string
	Type string
}

// endOf reads one end, accepting either spelling and preferring the sibling
// from_type/to_type field when the end itself carries no type.
func endOf(raw json.RawMessage, siblingType string) (end, error) {
	if len(raw) == 0 {
		return end{}, fmt.Errorf("relation end is missing")
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return end{Name: name, Type: siblingType}, nil
	}
	var obj struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Name != "" {
		if obj.Type == "" {
			obj.Type = siblingType
		}
		return end{Name: obj.Name, Type: obj.Type}, nil
	}
	return end{}, fmt.Errorf("relation end %s is neither a name nor an object with one", raw)
}

// parseReply turns whatever the model actually said into a reply, or says why
// it could not.
//
// Repair is worth the code because the alternative is not "a strict parser": it
// is a chunk of real content reported as empty (§5 forbids exactly that). But
// repair stops where guessing starts. A truncated reply is the case that
// decides the shape of this function — the entities before the cut are real,
// and returning them would deliver half a graph as though it were whole, which
// is the failure that looks like a success. So a reply that cannot be read
// whole is an error, and the caller turns it into an alchemy.Unread.
func parseReply(raw string) (reply, error) {
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "\ufeff"))
	if text == "" {
		return reply{}, fmt.Errorf("the model returned an empty reply")
	}

	// Markdown fences, "Here is the JSON:" preambles and trailing pleasantries
	// need no special cases: they are all just text outside the object, and a
	// scan that starts at a brace and ends at its match steps over all of them.
	starts := objectStarts(text)
	if len(starts) == 0 {
		return reply{}, fmt.Errorf("the reply contains no JSON object: %s", quoteForError(text))
	}
	var lastErr error
	for _, s := range starts {
		obj, ok := balancedObject(text, s)
		if !ok {
			lastErr = fmt.Errorf("the JSON object is unterminated, so the reply was truncated or cut off mid-write: %s", quoteForError(text))
			continue
		}
		r, err := decodeReply(obj)
		if err == nil {
			return r, nil
		}
		lastErr = fmt.Errorf("%w in: %s", err, quoteForError(obj))
	}
	return reply{}, lastErr
}

// decodeReply tries the object as written, then as repaired. Repair is second
// on purpose: a reply that is already valid JSON must never be rewritten, or
// fixing one model's habit corrupts another model's correct answer.
func decodeReply(obj string) (reply, error) {
	var r reply
	if err := json.Unmarshal([]byte(obj), &r); err == nil {
		return r, nil
	}
	repaired := removeTrailingCommas(singleToDoubleQuotes(obj))
	if err := json.Unmarshal([]byte(repaired), &r); err != nil {
		return reply{}, fmt.Errorf("the JSON could not be read even after repair: %v", err)
	}
	return r, nil
}

// objectStarts lists the brace positions worth trying, outermost first. More
// than one is tried because prose before the JSON can itself contain a brace,
// and giving up on the first candidate would report a readable reply as unread.
func objectStarts(text string) []int {
	var out []int
	for i, r := range text {
		if r == '{' {
			out = append(out, i)
		}
	}
	return out
}

// balancedObject returns text[start:end] for the object opening at start.
//
// It tracks double-quoted strings because a brace inside a name is not a
// brace: without this, an entity called "node-} {a" ends the object early and
// everything after it in the chunk is lost. Single-quoted strings are not
// tracked — a reply that both uses single quotes and puts a brace inside one is
// beyond honest repair, and it fails loudly here rather than being guessed at.
func balancedObject(text string, start int) (string, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// Nothing structural inside a string.
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}
	return "", false
}

// singleToDoubleQuotes rewrites 'single-quoted' strings as "double-quoted"
// ones. It only enters that mode outside a double-quoted string, which is what
// keeps an apostrophe in O'Brien from being read as the start of one.
func singleToDoubleQuotes(s string) string {
	var b strings.Builder
	inDouble := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inDouble {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '"':
			inDouble = true
			b.WriteByte(c)
		case '\'':
			b.WriteByte('"')
			i = copySingleQuoted(&b, s, i+1)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// copySingleQuoted copies the body of a 'single-quoted' string into b as the
// body of a double-quoted one, and returns the index of its closing quote.
func copySingleQuoted(b *strings.Builder, s string, i int) int {
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteByte(s[i])
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '\'':
			b.WriteByte('"')
			return i
		case '"':
			// A double quote inside what is about to become a double-quoted
			// string has to be escaped, or the repair produces JSON that is
			// broken in a new way.
			b.WriteString(`\"`)
		default:
			b.WriteByte(s[i])
		}
	}
	return i
}

// removeTrailingCommas drops the comma before a closing brace or bracket.
// Models emit them constantly; encoding/json rejects them outright.
func removeTrailingCommas(s string) string {
	var b strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && c == ',':
			if j := nextNonSpace(s, i+1); j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func nextNonSpace(s string, i int) int {
	for ; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
		default:
			return i
		}
	}
	return i
}

// quoteForError puts a bounded excerpt of the reply in the error. A person
// deciding whether the endpoint is broken or the prompt is wrong needs to see
// what came back; a person reading a log does not need all of it.
func quoteForError(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return fmt.Sprintf("%q", s)
	}
	// Cut on a rune boundary: the excerpt is read by a person, and a reply in
	// a language that is not ASCII should not arrive as escaped half-runes.
	r := []rune(s)
	if len(r) > max {
		r = r[:max]
	}
	return fmt.Sprintf("%q (excerpt; the reply was %d bytes)", string(r), len(s))
}
