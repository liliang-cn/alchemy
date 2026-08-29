package ontology_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// The prompt has to constrain, not merely inform. §5b: the extractor is
// constrained by the ontology and the verifier checks against it — the same
// list on both sides of the model — so the words that reach the model must be
// an instruction, and every type the verifier will accept must be in them.
func TestPromptConstrainsAndListsEveryType(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	got := prose.Prompt()

	if !strings.Contains(got, "Use ONLY these entity types") {
		t.Errorf("prompt does not constrain the entity vocabulary:\n%s", got)
	}
	if !strings.Contains(got, "Use ONLY these relation types") {
		t.Errorf("prompt does not constrain the relation vocabulary:\n%s", got)
	}
	for _, name := range []string{"Cluster", "Node", "StoragePool", "CONTAINS", "DEPLOYED_ON", "MENTIONS"} {
		if !strings.Contains(got, name) {
			t.Errorf("prompt omits the declared type %q; the verifier would accept what the model was never offered:\n%s", name, got)
		}
	}
	// A description is what makes "Node" mean a machine rather than a graph
	// node, and the attributes tell the model what is worth carrying.
	if !strings.Contains(got, "A set of nodes under one control plane.") {
		t.Errorf("prompt drops the entity descriptions:\n%s", got)
	}
	if !strings.Contains(got, "region") || !strings.Contains(got, "version") {
		t.Errorf("prompt drops the declared attributes:\n%s", got)
	}
	// Direction is the one thing only the prompt can prevent rather than
	// detect: a reversed edge is extracted, stored and only then reported.
	if !strings.Contains(got, "Cluster -> Node") {
		t.Errorf("prompt does not say which way CONTAINS runs:\n%s", got)
	}
}

// THE 74%-TO-94% LESSON, MADE EXECUTABLE.
//
// §2.1's third lesson, in the words of the person who learned it:
//
//	The fields above are the PROSE vocabulary: an LLM reads documentation and
//	emits Cluster, Node, DEPLOYED_ON. They are pasted into the extractor's
//	prompt ("Use ONLY these entity types"), which is exactly why a code
//	vocabulary cannot live there — telling a prose extractor it may emit
//	`function` and `calls` invites it to invent code structure out of
//	documentation.
//
// twoPartDoc declares both parts in one ontology, which is the situation the
// lesson is about. The prose prompt must carry the prose vocabulary and not
// one word of the code one, and the code prompt must do the reverse.
//
// The check is case-SENSITIVE on purpose. The prose part declares CONTAINS and
// the code part declares contains; they share a word and nothing else, and a
// case-insensitive search here would confuse the leak this test exists to
// catch with the correct behaviour it is checking.
func TestProsePromptNeverContainsTheCodeVocabulary(t *testing.T) {
	o := load(t, twoPartDoc)
	prose, err := o.Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatalf("Vocabulary(prose): %v", err)
	}
	code, err := o.Vocabulary(ontology.PartCode)
	if err != nil {
		t.Fatalf("Vocabulary(code): %v", err)
	}

	prosePrompt, codePrompt := prose.Prompt(), code.Prompt()

	for _, codeType := range []string{"file", "function", "contains", "calls"} {
		if word(codeType).MatchString(prosePrompt) {
			t.Errorf("the prose prompt offers the code type %q — this is exactly the invitation to "+
				"invent code structure out of documentation that the partition exists to prevent:\n%s",
				codeType, prosePrompt)
		}
	}
	for _, proseType := range []string{"Cluster", "Node", "StoragePool", "CONTAINS", "DEPLOYED_ON", "MENTIONS"} {
		if word(proseType).MatchString(codePrompt) {
			t.Errorf("the code prompt offers the prose type %q; the partition leaks in both directions:\n%s",
				proseType, codePrompt)
		}
	}
	// And each prompt does carry its own, so the test above is not passing by
	// rendering nothing at all.
	if !word("function").MatchString(codePrompt) {
		t.Errorf("the code prompt does not offer its own type \"function\":\n%s", codePrompt)
	}
	if !word("Cluster").MatchString(prosePrompt) {
		t.Errorf("the prose prompt does not offer its own type \"Cluster\":\n%s", prosePrompt)
	}
}

// An open relation must say it is open, in the prompt as well as in the
// checker. Dropping it would quietly shrink the vocabulary the model may use;
// inventing ends for it would be a lie; and "any entity type" without "listed
// above" would invite the model to name a type nobody declared.
func TestPromptSaysWhenARelationIsOpen(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	got := prose.Prompt()
	if !strings.Contains(got, "MENTIONS: any entity type listed above -> any entity type listed above") {
		t.Errorf("prompt does not say MENTIONS runs between any listed type:\n%s", got)
	}
}

// The whole prompt must be English: it is read by a model, and a vocabulary
// block that switches language mid-way is a prompt nobody can review.
func TestPromptIsASCIIEnglish(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	for _, r := range prose.Prompt() {
		if r > 127 {
			t.Fatalf("prompt contains the non-ASCII rune %q; the prompt text is English", r)
		}
	}
}

// A zero Vocabulary cannot come out of Load, but it can be built by hand, and
// "Use ONLY these entity types:" followed by nothing is not a weak constraint
// — it forbids everything and then lists no exception. oss-agent shipped that
// sentence and had to replace it.
func TestPromptOfAnEmptyVocabularyRefusesRatherThanContradicting(t *testing.T) {
	got := ontology.Vocabulary{}.Prompt()
	if strings.Contains(got, "Use ONLY these entity types") {
		t.Fatalf("an empty vocabulary rendered a constraint with nothing to satisfy it:\n%s", got)
	}
	if got == "" {
		t.Fatal("an empty vocabulary rendered an empty prompt; silence is the failure, not the fix")
	}
}

func word(s string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(s) + `\b`)
}

// A part may declare entity types and no relation types — a tabular part that
// only produces rows, say. The prompt must SAY so. Omitting the relation block
// leaves the model with an entity constraint and free rein on edges, which is
// the ungoverned half of the 74% graph, and every edge it invents comes back
// as a violation nobody can fix by editing the ontology.
func TestPromptSaysWhenNoRelationTypesAreDeclared(t *testing.T) {
	const doc = `{"id": "rows@1", "parts": {"tabular": {
		"entities": [{"name": "Customer"}], "relations": []}}}`
	v, _ := load(t, doc).Vocabulary(ontology.PartTabular)
	got := v.Prompt()

	if !strings.Contains(got, "Customer") {
		t.Fatalf("prompt omits the declared entity type:\n%s", got)
	}
	if !strings.Contains(got, "Do not extract any relations") {
		t.Fatalf("prompt leaves the relation vocabulary open by saying nothing about it:\n%s", got)
	}
}

// The prompt is a product surface, not an implementation detail: it is the
// only thing standing between a model and an unconstrained graph, and a change
// to its wording changes what comes back. Pinning it exactly means a reworded
// instruction has to be argued for in a diff rather than discovered in a
// compliance number three weeks later.
func TestPromptRendersExactly(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	const want = `Use ONLY these entity types. Any other type is a violation:
  Cluster - A set of nodes under one control plane. (attributes: region, version)
  Node - One machine in a cluster.
  StoragePool - Backing storage a node offers.

Use ONLY these relation types. Each line gives the ends the relation runs
between; extract it in that direction and never the reverse:
  CONTAINS: Cluster -> Node - A cluster holds every node under it.
  DEPLOYED_ON: StoragePool -> Node - A pool is deployed on a node.
  MENTIONS: any entity type listed above -> any entity type listed above - The text names one thing while describing another.

Spell every type exactly as written above. Do not coin a synonym, a plural or
a near-miss for a type on these lists, and leave out anything the text describes
that this vocabulary has no type for.
`
	if got := prose.Prompt(); got != want {
		t.Fatalf("prompt drifted.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
