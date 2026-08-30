package ontology

import (
	"fmt"
	"strings"
)

// Prompt renders one part's vocabulary for pasting into an extractor's system
// prompt. It is half of §5b's third mechanism -- the half the model sees -- and
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
// aliasNote licenses one field and fences it in the same sentence.
//
// A document that says "Ravel (formerly Tessera Manage)" is stating an identity
// fact, and without this the extractor has nowhere to put it: the alias is
// dropped, and the two names arrive as two nodes for a person to reconcile
// later out of a resemblance. Telling the model it may record one recovers a
// fact the source actually contains.
//
// The fence is the whole of why this is a constant and not a sentence somebody
// improvises. Alias is the one field nothing downstream can check: a type is
// checked against the vocabulary, an edge's ends are checked against the
// entities, and an invented alias is indistinguishable from a stated one
// forever after -- it is §2.1's guess wearing the badge of a fact about
// identity. So the instruction is not "record aliases", it is "record the ones
// the text says", with the phrases that count named, and an explicit refusal
// of the two things a model would otherwise do: shorten a name itself, and
// treat two things that resemble each other as one.
//
// What is NOT said here is that an alias makes two nodes one. It does not, and
// the model is not the party that would decide it: two things can share a name,
// so an alias meeting another node's name is alchemy.DuplicateAlias, which is a
// question for a person. The prompt stays silent on that because a model told
// its aliases will be used to merge things has been given a reason to be
// generous with them.
const aliasNote = "\nA thing may go by more than one name. When the text itself says so - " +
	"\"also known as\", \"formerly\", \"aka\", or a name in brackets after another - " +
	"put the other name in the entity's aliases list.\n" +
	"Record ONLY names the text states. Do not abbreviate a name yourself, do not " +
	"expand an abbreviation yourself, and do not add an alias because two things " +
	"look similar to you.\n"

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

	b.WriteString(aliasNote)

	if len(v.Relations) > 0 {
		// The ends are given because this is the only place a reversed edge
		// can be prevented rather than detected. Without them a backwards
		// relation is extracted, stored, and only then returned as a
		// violation for a person to fix by hand.
		b.WriteString(relationHeader(v.Relations))
		for _, r := range v.Relations {
			fmt.Fprintf(&b, "  %s: %s -> %s", r.Name, promptEnd(r.From), promptEnd(r.To))
			if r.BothWays {
				b.WriteString(" (either direction)")
			}
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

// relationHeader is the instruction above the relation list, and it says one
// more sentence than it used to only when some relation needs it.
//
// "Never the reverse" is false for a relation the ontology declared BothWays,
// and telling a model something false about the vocabulary it is constrained by
// is the 74% mechanism running backwards: asked to extract `imports` under that
// sentence, a model drops the second half of every mutual pair and the graph
// silently disagrees with the source it was read from.
//
// The exception is not offered where nothing claims it. A prompt is a product
// surface -- it decides what comes back -- so a vocabulary with no both-ways
// relation renders exactly the bytes it always did, and no extraction already
// in production changes because this field was added. A clause offered where
// nothing needs it would also be an invitation to go looking for permission.
//
// The last clause is the one that keeps BothWays from being read as symmetry.
// "Both may be true at once" is not "one implies the other": a model that
// materialised the reverse of an edge the text never stated would be writing an
// edge no source asserted, which is the one thing §5b promises a reader it can
// always attribute.
func relationHeader(relations []RelationType) string {
	const opening = "\nUse ONLY these relation types. Each line gives the ends the relation runs\n" +
		"between; extract it in that direction and never the reverse"
	for _, r := range relations {
		if r.BothWays {
			return opening + ". A line marked\n" +
				"(either direction) may run either way and both ways may be true at once, but\n" +
				"extract only the direction the text states:\n"
		}
	}
	return opening + ":\n"
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

const noTypes = "This vocabulary declares no types, so nothing may be extracted under it.\n"

// TablePrompt renders the same vocabulary for the question a table asks.
//
// A column mapping is not an extraction. A prose extractor is asked what a
// passage says; a tabular reader is asked what a *row* is and what each
// *column* means, and it answers with a mapping rather than with records. The
// types it may answer with are the same types, checked by the same
// AllowsEntity/AllowsRelation, so the lists below are rendered by the same code
// from the same value -- only the question they are offered as an answer to
// differs.
//
// It lives here, and not in the tabular reader, for the reason the package
// exists: a second wording of the vocabulary is a second ontology that nothing
// checks against the first. A reader that phrased the list in its own words
// would be constraining the model against something the verifier never sees.
//
// The direction sentence is the one that genuinely had to change. In prose the
// model chooses both ends, so "never the reverse" is advice about its own
// output. In a table one end is fixed before any column is looked at -- it is
// whatever the row was called -- so the declared ends are what decide whether a
// relation type is available for a column at all, and the sentence has to say
// which end is which.
func (v Vocabulary) TablePrompt() string {
	if len(v.Entities) == 0 {
		return noTypes
	}
	var b strings.Builder
	b.WriteString("A row of this table becomes one entity, and each column becomes part of it: its\n" +
		"identity, its name, a plain attribute, or an edge to another thing the table\n" +
		"names by identifier. Type all of them from these lists and nothing else.\n\n")
	b.WriteString("Use ONLY these entity types. Any other type is a violation:\n")
	v.writeEntities(&b)

	if len(v.Relations) > 0 {
		b.WriteString("\nUse ONLY these relation types. Each line gives the ends the relation runs\n" +
			"between: a column may become that edge only when the row's own entity type is\n" +
			"the left end and the column's target type is the right end, never the reverse:\n")
		v.writeRelations(&b)
	} else {
		b.WriteString("\nDo not map any column to a relation: this vocabulary declares no relation types.\n")
	}

	// "any column" rather than "anything the text describes": a table has
	// columns and no text, and telling a mapper to leave out what the text
	// describes leaves it to decide what that means about a column.
	writeSpelling(&b, "any column this vocabulary\nhas no type for")
	return b.String()
}

// writeEntities and writeRelations are the vocabulary itself. Both prompts call
// them, so the names, descriptions, attributes and ends a model is shown are
// produced in exactly one place however many questions this package learns to
// ask.
func (v Vocabulary) writeEntities(b *strings.Builder) {
	for _, e := range v.Entities {
		fmt.Fprintf(b, "  %s", e.Name)
		if e.Description != "" {
			fmt.Fprintf(b, " - %s", e.Description)
		}
		if len(e.Attributes) > 0 {
			fmt.Fprintf(b, " (attributes: %s)", strings.Join(e.Attributes, ", "))
		}
		b.WriteString("\n")
	}
}

// writeRelations names each type, its ends, and whether it may run either way.
//
// AtMostOneIn and AtMostOneOut are deliberately NOT written, and the omission
// is the decision. BothWays is here because it WITHHOLDS a contradiction -- it
// tells the model nothing to do -- while a cardinality is a rule the model
// would act on, and the action it would take is to drop the second claim. That
// is precisely the record the constraint exists to surface: a profile saying
// Ada is CTO and a correction saying Bruno is are two sources disagreeing, and
// §7.3 stops the job on it so a person decides. An extractor told "a company
// has one CTO" resolves it silently, in one chunk, with no provenance and no
// question -- the confident wrong answer with a citation that §2.1 is about.
//
// So cardinality is a checker's rule and never an extractor's. The same list
// on both sides of the model (§5) is about which types exist, not about how
// many of each the model should emit.
func (v Vocabulary) writeRelations(b *strings.Builder) {
	for _, r := range v.Relations {
		fmt.Fprintf(b, "  %s: %s -> %s", r.Name, promptEnd(r.From), promptEnd(r.To))
		if r.BothWays {
			b.WriteString(" (either direction)")
		}
		if r.Description != "" {
			fmt.Fprintf(b, " - %s", r.Description)
		}
		b.WriteString("\n")
	}
}

// writeSpelling names the near-misses, which is worth a line: the types a model
// invents are rarely wild, they are plurals, synonyms and re-spellings of what
// it was just given, and each one becomes a violation a person has to read.
//
// leaveOut is what the caller's kind of source has that the vocabulary cannot
// express -- a sentence for prose, a column for a table -- because "leave it out"
// is only actionable when the thing being left out is named.
func writeSpelling(b *strings.Builder, leaveOut string) {
	fmt.Fprintf(b, "\nSpell every type exactly as written above. Do not coin a synonym, a plural or\n"+
		"a near-miss for a type on these lists, and leave out %s.\n", leaveOut)
}
