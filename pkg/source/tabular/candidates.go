package tabular

import "strings"

// This file is the deterministic half of inference. Nothing here ever chooses a
// mapping — choosing by a rule over column names is exactly the failure
// DESIGN.md §2.1 describes, because "id" scores the same however many columns
// end in "_id". What it produces is the list of columns that could have filled
// a role, which is what an alchemy.Guess's Alternatives must carry: a guess
// naming no alternative tells a reviewer nothing about what else it could have
// been.

// identifierCandidates are the columns that could plausibly identify something.
// Order is header order, and header order is never a tie-break.
func identifierCandidates(head []string) []string {
	var out []string
	for _, h := range head {
		if looksLikeIdentifier(h) {
			out = append(out, h)
		}
	}
	return out
}

func looksLikeIdentifier(col string) bool {
	c := strings.ToLower(strings.TrimSpace(col))
	switch c {
	case "id", "uuid", "guid", "key", "code", "no", "number", "pk":
		return true
	}
	for _, suffix := range []string{"_id", "_uuid", "_guid", "_key", "_code", "_no", "_number", "id"} {
		if strings.HasSuffix(c, suffix) && len(c) > len(suffix) {
			return true
		}
	}
	return false
}

// nameCandidates are the columns that could plausibly be what a person calls
// the row.
func nameCandidates(head []string) []string {
	var out []string
	for _, h := range head {
		if looksLikeName(h) {
			out = append(out, h)
		}
	}
	return out
}

func looksLikeName(col string) bool {
	c := strings.ToLower(strings.TrimSpace(col))
	for _, part := range []string{"name", "title", "label", "description", "subject"} {
		if strings.Contains(c, part) {
			return true
		}
	}
	return false
}

// without drops one entry, so Alternatives holds what was *not* chosen.
func without(all []string, chosen string) []string {
	var out []string
	for _, c := range all {
		if c != chosen {
			out = append(out, c)
		}
	}
	return out
}

// substringOf names the candidates the chosen column's name is a substring of.
// It exists only to write the reason in §2.1's own terms: "id" being a substring
// of "order_id" and "product_id" is the specific shape of ambiguity that a
// naive matcher resolves by position and nobody hears about.
func substringOf(chosen string, others []string) []string {
	var out []string
	c := strings.ToLower(chosen)
	for _, o := range others {
		if o != chosen && strings.Contains(strings.ToLower(o), c) {
			out = append(out, o)
		}
	}
	return out
}
