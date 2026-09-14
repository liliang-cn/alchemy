package extract

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// PromptVersion changes whenever the text below changes in a way that could
// change a model's answer.
//
// It is exported because §8.2's content-addressed cache is keyed on hash of
// (chunk text, model, ontology version, prompt version), and that key is built
// by the service rather than here. A cache that survives a prompt change is a
// cache that returns the old prompt's opinion, so the version has to be
// reachable from outside this package or the guarantee is unkeepable.
//
// extract/2 added the standing-answers section (see standingAnswers): a chunk
// asked under a reviewer's `always` rule is asked a different question from
// the same chunk asked before anybody had decided anything, and a cache that
// survived the change would answer the new question with the old opinion.
const PromptVersion = "extract/2"

// chunkMarker labels the chunk index in the user prompt.
//
// It is there for the model — an answer about "chunk 14" is easier to trace
// back than an answer about "the text" — and it is also the only handle a
// reader of a transcript has for lining a reply up with the chunk that caused
// it, which matters because the calls do not finish in order (§8.2).
const chunkMarker = "Chunk index: "

// systemPrompt frames the task and carries the vocabulary.
//
// The vocabulary is pasted in as ontology.Vocabulary.Prompt() wrote it and is
// never paraphrased here. That is §5b's third mechanism: the extractor and the
// verifier read the same list, and a second wording of it in this package
// would be a second ontology that nothing checks against the first.
func systemPrompt(v ontology.Vocabulary, told []string) string {
	var b strings.Builder
	b.WriteString("You extract a knowledge graph from one chunk of a document, under a closed\n" +
		"vocabulary. Extract only what the chunk itself states. Do not add what you know\n" +
		"about the subject from anywhere else.\n\n")
	b.WriteString(v.Prompt())
	b.WriteString("\nReply with one JSON object and nothing else: no prose before or after it, and\n" +
		"no markdown fence. This is the shape:\n\n" +
		`{"entities": [
   {"type": "<an entity type from the list above>",
    "name": "<the name exactly as this chunk writes it>",
    "attributes": {"<a declared attribute>": "<the value this chunk states>"},
    "confidence": 0.0}
 ],
 "relations": [
   {"type": "<a relation type from the list above>",
    "from": "<name of the entity the relation starts at>",
    "from_type": "<that entity's type>",
    "to": "<name of the entity the relation ends at>",
    "to_type": "<that entity's type>",
    "confidence": 0.0}
 ]}` + "\n\n")
	// Each of these lines pays for itself downstream. Consistent spelling is
	// what lets one thing named in two chunks merge into one node; typed
	// relation ends are what lets an end that was never listed as an entity
	// still resolve to the right node; omitted rather than invented confidence
	// keeps Provenance.Confidence meaningful; the permission to answer nothing
	// keeps ChunksEmpty a fact about the documents rather than about the
	// vocabulary; and asking for what the vocabulary cannot say is what makes
	// a missing word a finding instead of a silence.
	//
	// The first of them is a nudge and not a guarantee, for the same reason
	// standingAnswers is: a chunk is a separate call that cannot see what the
	// others were told, so "use that same spelling every time" is advice about
	// a decision the model is making without the information the advice needs.
	// It is worth asking for and it is not worth believing — one real run put
	// roughly one node in six on the wrong side of it. What catches what this
	// line misses is verify's duplicate scan, which reports the pairs rather
	// than joining them (see pkg/verify/duplicates.go and alchemy.Duplicate):
	// the two names are evidence, and no rule that turns one into the other
	// can tell "document package" from "language model".
	b.WriteString("- Write every name exactly as the chunk writes it, and use that same spelling\n" +
		"  every time the same thing appears, so that one thing does not become two.\n" +
		"- Give from_type and to_type on every relation, including when that entity is\n" +
		"  not in your entities list.\n" +
		"- attributes and confidence are optional. Omit an attribute the chunk does not\n" +
		"  state, and omit confidence rather than inventing a number for it; when you do\n" +
		"  give it, it is your own confidence between 0 and 1.\n" +
		"- If the chunk states something about a thing that its type has no attribute\n" +
		"  for, put it in attributes anyway under the name you would have declared. The\n" +
		"  same rule as above applies to it: an undeclared name is checked, named and\n" +
		"  shown to a person, and a detail left out because there was no field for it is\n" +
		"  one nobody downstream can find.\n")
	// The line this replaced said an unexpressible chunk was an empty chunk,
	// and that instruction is what made every mechanism downstream of it
	// unreachable — see TestThePromptAsksForWhatTheVocabularyCannotSay.
	b.WriteString("- If the chunk states a relationship these types cannot express, write it in\n" +
		"  relations anyway, using the type you would have declared for it, in capitals.\n" +
		"  The vocabulary is checked after you, so an undeclared type is named, shown to\n" +
		"  a person and can be added — while a fact you leave out because there was no\n" +
		"  word for it is one nobody downstream can find. The same goes for an entity\n" +
		"  whose type is not listed.\n" +
		"  This is not permission to invent: report only what the chunk states, and use\n" +
		"  a declared type whenever one fits. A type you report and a person rejects\n" +
		"  costs them one answer; a fact you drop costs them the fact.\n")
	// The one stage where a model decides something, asked to say what it
	// decided. Everything that reads this was already built — the review
	// queue's KindGuess, the verbs that answer one, the ledger entry — and
	// only the tabular and graph-import producers ever raised one, so a run
	// reporting "guesses 0" meant nobody had asked rather than nothing had
	// been guessed.
	b.WriteString("- When a sentence can be read in more than one way under this vocabulary and\n" +
		"  you had to pick one, put it in guesses with the alternatives you did not use.\n" +
		"  A person reviewing this reads those first. Report a real choice and not a\n" +
		"  formality: a reading nothing else competed with is not a guess, and a list of\n" +
		"  those would bury the ones that are.\n" +
		"- If the chunk states nothing at all, reply\n" +
		`  {"entities": [], "relations": [], "guesses": []}` + ". That is a correct answer\n" +
		"  for a chunk with nothing in it, and only for that.\n")
	b.WriteString(standingAnswers(told))
	return b.String()
}

// standingAnswers is what a reviewer has already decided, put in front of the
// model.
//
// §6's first reason for gRPC is that "an extractor that has already learned
// 'this is not an entity' should stop proposing it in the next chunk", and
// this is the half of that which reaches the model. It is deliberately last in
// the prompt: it is the most specific instruction in it, it contradicts
// nothing above it, and a model reading a closed vocabulary and then a list of
// exceptions to how it has been applied is reading them in the order a person
// would say them.
//
// It is a nudge and is documented as one. A model may propose the thing
// anyway, which is why the same snapshot also carries a filter that the answer
// is put through before it enters the graph (see Settled). Telling the model
// is the cheap half; not believing it is the honest half.
//
// An empty list writes nothing at all rather than an empty heading. A prompt
// that says "the reviewer has decided:" and then stops is an instruction to
// wonder what was left out.
func standingAnswers(told []string) string {
	if len(told) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nA person reviewing this corpus has already settled the following, and their\n" +
		"decisions stand. Do not propose again what they have ruled on:\n")
	for _, line := range told {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	return b.String()
}

// userPrompt is the chunk itself, with the little the model needs to read it in
// context: where it came from, which chunk it is, and the section it sits
// under. The heading is included because a chunk cut out of a section reads
// blind without it — "it runs on node-a" resolves against "## SuperAI" and
// against nothing else.
func userPrompt(c alchemy.Chunk) string {
	var b strings.Builder
	if c.Source != "" {
		fmt.Fprintf(&b, "Source: %s\n", c.Source)
	}
	fmt.Fprintf(&b, "%s%d\n", chunkMarker, c.Index)
	if c.Heading != "" {
		fmt.Fprintf(&b, "Section: %s\n", c.Heading)
	}
	// The chunk is delimited rather than merely appended: a chunk of a
	// document about JSON extraction will contain sentences that read as
	// instructions, and the markers are what keep them being text.
	b.WriteString("\nThe chunk to extract from is between the markers.\n")
	b.WriteString("---BEGIN CHUNK---\n")
	b.WriteString(c.Text)
	if !strings.HasSuffix(c.Text, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("---END CHUNK---\n")
	return b.String()
}
