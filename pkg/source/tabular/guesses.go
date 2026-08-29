package tabular

import (
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Guesses answer two different questions, and a reviewer can tell them apart by
// ChosenAs:
//
//   - "id_column" / "name_column" ask which *column* filled a role, so
//     Alternatives holds the other columns that could have. This is §2.1's
//     guess: "id" won over "order_id" and "product_id", and the reason says so.
//   - "attribute:x" / "relation:T->X" ask what *role* a column was given, so
//     Alternatives holds the other roles it plausibly could have had.
//
// Alternatives is empty only when the deterministic ranker in candidates.go
// found no other plausible outcome — for a column like "qty" there is none, and
// padding the list would make the guesses that matter harder to find.

func guessesFor(source string, head []string, m *Mapping, p proposal, prov alchemy.Provenance) []alchemy.Guess {
	var out []alchemy.Guess
	out = append(out, alchemy.Guess{
		Field:        source,
		ChosenAs:     m.EntityType,
		Alternatives: without(typeCandidates(source, p.EntityType), m.EntityType),
		Reason:       reasonOr(p, "entity_type", fmt.Sprintf("a row of %s was called a %s; the table never says what a row is", source, m.EntityType)),
		Provenance:   prov,
	})

	ids := identifierCandidates(head)
	out = append(out, alchemy.Guess{
		Field:        m.IDColumn,
		ChosenAs:     "id_column",
		Alternatives: without(ids, m.IDColumn),
		Reason:       idReason(p, m.IDColumn, ids),
		Provenance:   prov,
	})

	if m.NameColumn != "" {
		names := nameCandidates(head)
		out = append(out, alchemy.Guess{
			Field:        m.NameColumn,
			ChosenAs:     "name_column",
			Alternatives: without(names, m.NameColumn),
			Reason:       reasonOr(p, "name_column", "chosen as what a person calls the row"),
			Provenance:   prov,
		})
	}

	for _, col := range sortedKeys(m.Attributes) {
		out = append(out, alchemy.Guess{
			Field:        col,
			ChosenAs:     "attribute:" + m.Attributes[col],
			Alternatives: otherRoles(col, m, "attribute:"+m.Attributes[col]),
			Reason:       reasonOr(p, col, "kept as a plain value on the row's own entity"),
			Provenance:   prov,
		})
	}

	for _, r := range m.Relations {
		chosen := "relation:" + r.RelationType + "->" + r.TargetType
		out = append(out, alchemy.Guess{
			Field:        r.Column,
			ChosenAs:     chosen,
			Alternatives: otherRoles(r.Column, m, chosen),
			Reason:       reasonOr(p, r.Column, fmt.Sprintf("read as an edge to a %s rather than a value on this row", r.TargetType)),
			Provenance:   prov,
		})
	}
	return out
}

// idReason states the tie-break in §2.1's own terms when the shape matches: a
// column whose name is a substring of the columns it beat is the case where a
// matcher choosing by position runs cleanly either way.
func idReason(p proposal, chosen string, ids []string) string {
	base := reasonOr(p, "id_column", "chosen as the column that identifies the row itself")
	others := without(ids, chosen)
	if len(others) == 0 {
		return base + "; it was the only identifier-shaped column"
	}
	if sub := substringOf(chosen, others); len(sub) > 0 {
		return fmt.Sprintf("%s; %q is also a substring of %s, so a matcher choosing by column order would have taken whichever came first and run just as cleanly",
			base, chosen, strings.Join(sub, " and "))
	}
	return fmt.Sprintf("%s; it was chosen over %s", base, strings.Join(others, ", "))
}

// otherRoles is what else this column could have been. A column shaped like an
// identifier is the one worth listing: it is the column a mapping gets wrong.
func otherRoles(col string, m *Mapping, chosen string) []string {
	var out []string
	if looksLikeIdentifier(col) {
		out = append(out, "id_column")
		if !strings.HasPrefix(chosen, "relation:") {
			out = append(out, "relation:->"+strings.TrimSuffix(strings.TrimSuffix(col, "_id"), "id"))
		}
		if !strings.HasPrefix(chosen, "attribute:") {
			out = append(out, "attribute:"+col)
		}
	}
	if looksLikeName(col) && m.NameColumn != col {
		out = append(out, "name_column")
	}
	if attr, ok := m.Attributes[col]; ok && attr != col {
		// Renaming a column is itself a decision; keeping the source name was
		// the alternative that needed no judgement.
		out = append(out, "attribute:"+col)
	}
	return dedupeStrings(out)
}

// typeCandidates are the entity type names the source itself suggests. A model
// that answers "Order" for line_items.csv has made a decision, and a reviewer
// should see what the file name would have said.
func typeCandidates(source, hint string) []string {
	base := source
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	var out []string
	if c := camel(base); c != "" {
		out = append(out, c, singular(c))
	}
	if hint != "" {
		out = append(out, hint)
	}
	return dedupeStrings(out)
}

func camel(s string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == ' ' }) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses") && len(s) > 3:
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 1:
		return s[:len(s)-1]
	}
	return s
}

func reasonOr(p proposal, key, fallback string) string {
	if r := strings.TrimSpace(p.Reasons[key]); r != "" {
		return r
	}
	return fallback
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupeStrings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
