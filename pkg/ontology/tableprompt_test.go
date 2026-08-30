package ontology_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// A table asks a different question from a document. A prose extractor is asked
// "what does this text say"; a tabular reader is asked "what is a row, and what
// does each column mean". The types it may answer with are the same types, so
// they are rendered by the same code from the same Vocabulary — what differs is
// the question they are offered as an answer to.
//
// The reason this lives in pkg/ontology rather than in pkg/source/tabular is
// §5b's third mechanism: a second wording of the vocabulary is a second
// ontology that nothing checks against the first. Only one package writes the
// list down.
func TestTablePromptConstrainsTheMappingToTheDeclaredTypes(t *testing.T) {
	v, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	got := v.TablePrompt()

	if !strings.Contains(got, "Use ONLY these entity types") {
		t.Errorf("the table prompt does not constrain what a row may become:\n%s", got)
	}
	if !strings.Contains(got, "Use ONLY these relation types") {
		t.Errorf("the table prompt does not constrain what a column may become:\n%s", got)
	}
	// It has to name the two things a mapping decides, or it is a list of types
	// with no bearing on the question being asked.
	if !strings.Contains(got, "row") || !strings.Contains(got, "column") {
		t.Errorf("the table prompt never says a row or a column is what is being typed:\n%s", got)
	}
	for _, name := range []string{"Cluster", "Node", "StoragePool", "CONTAINS", "DEPLOYED_ON", "MENTIONS"} {
		if !strings.Contains(got, name) {
			t.Errorf("the table prompt omits the declared type %q; the verifier would accept "+
				"what the model was never offered:\n%s", name, got)
		}
	}
	// Descriptions and ends carry over for the same reasons they do in Prompt:
	// "Node" means a machine because of its description, and the ends are what
	// tell the reader which relation type fits a column at all.
	if !strings.Contains(got, "A set of nodes under one control plane.") {
		t.Errorf("the table prompt drops the entity descriptions:\n%s", got)
	}
	if !strings.Contains(got, "Cluster -> Node") {
		t.Errorf("the table prompt does not say which way CONTAINS runs:\n%s", got)
	}
}

// The partition is structural, and a second renderer is a second chance to
// leak across it. §2.1's third lesson holds for whichever prompt is being built.
func TestTablePromptNeverCarriesAnotherPartsVocabulary(t *testing.T) {
	o := load(t, twoPartDoc)
	prose, err := o.Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatalf("Vocabulary(prose): %v", err)
	}
	code, err := o.Vocabulary(ontology.PartCode)
	if err != nil {
		t.Fatalf("Vocabulary(code): %v", err)
	}
	prosePrompt, codePrompt := prose.TablePrompt(), code.TablePrompt()
	for _, codeType := range []string{"file", "function", "contains", "calls"} {
		if word(codeType).MatchString(prosePrompt) {
			t.Errorf("the prose table prompt offers the code type %q:\n%s", codeType, prosePrompt)
		}
	}
	for _, proseType := range []string{"Cluster", "StoragePool", "DEPLOYED_ON"} {
		if word(proseType).MatchString(codePrompt) {
			t.Errorf("the code table prompt offers the prose type %q:\n%s", proseType, codePrompt)
		}
	}
}

// A part with entity types and no relation types must say so, or the reader is
// constrained on rows and free on columns — the ungoverned half of the 74%
// graph, in a mapping instead of an extraction.
func TestTablePromptSaysWhenNoRelationTypesAreDeclared(t *testing.T) {
	const doc = `{"id": "rows@1", "parts": {"tabular": {
		"entities": [{"name": "Customer"}], "relations": []}}}`
	v, _ := load(t, doc).Vocabulary(ontology.PartTabular)
	got := v.TablePrompt()
	if !strings.Contains(got, "Customer") {
		t.Fatalf("the table prompt omits the declared entity type:\n%s", got)
	}
	if !strings.Contains(got, "Do not map any column to a relation") {
		t.Fatalf("the table prompt leaves the relation vocabulary open by saying nothing "+
			"about it:\n%s", got)
	}
}

// The same refusal Prompt makes. "Use ONLY these entity types:" with nothing
// under it forbids everything and permits nothing, and a model resolves that
// contradiction by ignoring the block.
func TestTablePromptOfAnEmptyVocabularyRefusesRatherThanContradicting(t *testing.T) {
	got := ontology.Vocabulary{}.TablePrompt()
	if strings.Contains(got, "Use ONLY these entity types") {
		t.Fatalf("an empty vocabulary rendered a constraint with nothing to satisfy it:\n%s", got)
	}
	if got == "" {
		t.Fatal("an empty vocabulary rendered an empty prompt; silence is the failure, not the fix")
	}
}

// English, for the same reason Prompt is: it is read by a model, and a prompt
// that switches language mid-way is one nobody can review.
func TestTablePromptIsASCIIEnglish(t *testing.T) {
	v, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	for _, r := range v.TablePrompt() {
		if r > 127 {
			t.Fatalf("the table prompt contains the non-ASCII rune %q", r)
		}
	}
}

// Pinned exactly, for the reason TestPromptRendersExactly pins the other one:
// the prompt is the only thing standing between a model and an unconstrained
// graph, so a reworded instruction has to be argued for in a diff rather than
// discovered in a compliance number three weeks later.
func TestTablePromptRendersExactly(t *testing.T) {
	v, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	const want = `A row of this table becomes one entity, and each column becomes part of it: its
identity, its name, a plain attribute, or an edge to another thing the table
names by identifier. Type all of them from these lists and nothing else.

Use ONLY these entity types. Any other type is a violation:
  Cluster - A set of nodes under one control plane. (attributes: region, version)
  Node - One machine in a cluster.
  StoragePool - Backing storage a node offers.

Use ONLY these relation types. Each line gives the ends the relation runs
between: a column may become that edge only when the row's own entity type is
the left end and the column's target type is the right end, never the reverse:
  CONTAINS: Cluster -> Node - A cluster holds every node under it.
  DEPLOYED_ON: StoragePool -> Node - A pool is deployed on a node.
  MENTIONS: any entity type listed above -> any entity type listed above - The text names one thing while describing another.

Spell every type exactly as written above. Do not coin a synonym, a plural or
a near-miss for a type on these lists, and leave out any column this vocabulary
has no type for.
`
	if got := v.TablePrompt(); got != want {
		t.Fatalf("the table prompt drifted.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
