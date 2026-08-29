package ontology

import (
	"fmt"
	"strings"
)

// Prompt renders one part's vocabulary for pasting into an extractor's system
// prompt. It is half of §5b's third mechanism — the half the model sees — and
// AllowsEntity/AllowsRelation are the other half. Both read the same
// Vocabulary value, which is what "the same list on both sides of the model"
// means in code.
//
// It renders the part it was called on and nothing else. There is no argument
// by which a caller could ask it for another part's types, because a
// Vocabulary does not know the ontology it came from.
//
// The text is English because it is read by a model, and it is an instruction
// rather than a description because a list offered without "use ONLY these" is
// a suggestion, and the 74% graph was extracted under a suggestion.
func (v Vocabulary) Prompt() string {
	if len(v.Entities) == 0 {
		// "Use ONLY these entity types:" followed by nothing forbids
		// everything and permits nothing, which is worse than no constraint:
		// the model resolves the contradiction by ignoring the whole block.
		// Load makes this unreachable; a hand-built Vocabulary can still get
		// here, and it must say so rather than emit the broken sentence.
		return "This vocabulary declares no types, so nothing may be extracted under it.\n"
	}

	var b strings.Builder
	b.WriteString("Use ONLY these entity types. Any other type is a violation:\n")
	for _, e := range v.Entities {
		fmt.Fprintf(&b, "  %s", e.Name)
		if e.Description != "" {
			fmt.Fprintf(&b, " - %s", e.Description)
		}
		if len(e.Attributes) > 0 {
			fmt.Fprintf(&b, " (attributes: %s)", strings.Join(e.Attributes, ", "))
		}
		b.WriteString("\n")
	}

	if len(v.Relations) > 0 {
		// The ends are given because this is the only place a reversed edge
		// can be prevented rather than detected. Without them a backwards
		// relation is extracted, stored, and only then returned as a
		// violation for a person to fix by hand.
		b.WriteString("\nUse ONLY these relation types. Each line gives the ends the relation runs\n" +
			"between; extract it in that direction and never the reverse:\n")
		for _, r := range v.Relations {
			fmt.Fprintf(&b, "  %s: %s -> %s", r.Name, promptEnd(r.From), promptEnd(r.To))
			if r.Description != "" {
				fmt.Fprintf(&b, " - %s", r.Description)
			}
			b.WriteString("\n")
		}
	} else {
		// Saying nothing here would leave the model constrained on nodes and
		// free on edges, which is the ungoverned half of the 74% graph. It is
		// also unfixable downstream: every invented edge type becomes a
		// violation, and no edit to the ontology makes it go away.
		b.WriteString("\nDo not extract any relations: this vocabulary declares no relation types.\n")
	}

	// Naming the near-misses is worth a line: the types a model invents are
	// rarely wild, they are plurals, synonyms and re-spellings of what it was
	// just given, and each one becomes a violation a person has to read.
	b.WriteString("\nSpell every type exactly as written above. Do not coin a synonym, a plural or\n" +
		"a near-miss for a type on these lists, and leave out anything the text describes\n" +
		"that this vocabulary has no type for.\n")
	return b.String()
}

// promptEnd renders one end of a relation for the model.
//
// "any entity type listed above" rather than "any entity type": the open end
// is bounded by the entity list in the same prompt, and the shorter phrasing
// would invite exactly the invention the block is there to prevent.
func promptEnd(declared []string) string {
	if len(declared) == 0 {
		return "any entity type listed above"
	}
	return strings.Join(declared, "|")
}
