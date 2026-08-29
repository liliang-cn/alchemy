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
const PromptVersion = "extract/1"

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
func systemPrompt(v ontology.Vocabulary) string {
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
	// Each of these four lines pays for itself downstream. Consistent spelling
	// is what lets one thing named in two chunks merge into one node; typed
	// relation ends are what lets an end that was never listed as an entity
	// still resolve to the right node; omitted rather than invented confidence
	// keeps Provenance.Confidence meaningful; and the permission to answer
	// nothing is what keeps ChunksEmpty a fact about the documents.
	b.WriteString("- Write every name exactly as the chunk writes it, and use that same spelling\n" +
		"  every time the same thing appears, so that one thing does not become two.\n" +
		"- Give from_type and to_type on every relation, including when that entity is\n" +
		"  not in your entities list.\n" +
		"- attributes and confidence are optional. Omit an attribute the chunk does not\n" +
		"  state, and omit confidence rather than inventing a number for it; when you do\n" +
		"  give it, it is your own confidence between 0 and 1.\n" +
		"- If the chunk states nothing this vocabulary can express, reply\n" +
		`  {"entities": [], "relations": []}` + ". An empty answer is a correct answer\n" +
		"  here. An invented one is not.\n")
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
