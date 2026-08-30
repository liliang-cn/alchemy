package ontology_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// codeDoc declares a relation that runs both ways and one that does not, in the
// vocabulary the case came from: a code graph, where two files importing each
// other is ordinary and a file containing a class is not reversible.
const codeDoc = `{
  "id": "code@1",
  "parts": {
    "code": {
      "entities": [{"name": "file"}, {"name": "class"}],
      "relations": [
        {"name": "imports", "description": "One file imports another.",
         "from": ["file"], "to": ["file"], "both_ways": true},
        {"name": "contains", "from": ["file"], "to": ["class"]}
      ]
    }
  }
}`

func codeVocab(t *testing.T) ontology.Vocabulary {
	t.Helper()
	v, err := load(t, codeDoc).Vocabulary(ontology.PartCode)
	if err != nil {
		t.Fatalf("Vocabulary(code): %v", err)
	}
	return v
}

// The declaration has to survive the document. Load reads with
// DisallowUnknownFields, so a spelling this package does not know is rejected
// rather than silently dropped — which is the same protection "form" instead of
// "from" already gets, and for the same reason: a dropped declaration widens
// nothing and narrows nothing visibly, it just re-arms the false conflict.
func TestLoadReadsBothWays(t *testing.T) {
	v := codeVocab(t)
	for _, tc := range []struct {
		name string
		want bool
	}{{"imports", true}, {"contains", false}} {
		if got := v.RunsBothWays(tc.name); got != tc.want {
			t.Errorf("RunsBothWays(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The default is the whole question, because every ontology written before this
// field existed says nothing. A relation type that says nothing runs one way —
// which is exactly what its prompt has always told the model ("extract it in
// that direction and never the reverse"), so no existing ontology changes
// meaning and §5c's foreign-key-against-a-document finding keeps firing.
func TestARelationThatSaysNothingRunsOneWay(t *testing.T) {
	prose, err := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CONTAINS", "DEPLOYED_ON", "MENTIONS"} {
		if prose.RunsBothWays(name) {
			t.Errorf("RunsBothWays(%q) = true; a type that says nothing must not acquire a claim it never made", name)
		}
	}
}

// An undeclared type is not a one-way type and not a both-ways type: the
// question does not apply to it. RunsBothWays answers what the vocabulary
// declares, so a caller that needs "was this claimed asymmetric" asks
// RunsOneWay, which is false for a type nobody declared — an ontology nobody
// claimed has no rules to break.
func TestAnUndeclaredTypeIsNeitherOneWayNorBothWays(t *testing.T) {
	v := codeVocab(t)
	if v.RunsBothWays("depends_on") {
		t.Error("RunsBothWays on an undeclared type = true; nothing declared it anything")
	}
	if v.RunsOneWay("depends_on") {
		t.Error("RunsOneWay on an undeclared type = true; asserting asymmetry about a type nobody declared is a rule nobody wrote")
	}
	if !v.RunsOneWay("contains") {
		t.Error("RunsOneWay(\"contains\") = false; a declared type that says nothing runs one way")
	}
	if v.RunsOneWay("imports") {
		t.Error("RunsOneWay(\"imports\") = true; the ontology said it runs both ways")
	}
}

// Type names are matched case-insensitively everywhere else in this package,
// and a claim that only holds for one spelling is a claim that fails on the
// model's next capitalisation wobble.
func TestRunsBothWaysFoldsCase(t *testing.T) {
	v := codeVocab(t)
	if !v.RunsBothWays("IMPORTS") {
		t.Error("RunsBothWays(\"IMPORTS\") = false; names fold here as they do everywhere else")
	}
}

// If the ontology says a relation may run both ways, both ways are allowed.
// Anything else would move the noise one field over: every second edge of a
// mutual pair would come back as a ViolationRelationNotAllowed, and the
// declaration would be contradicted by the checker that read it.
func TestAllowsRelationAcceptsBothEndsOfABothWaysType(t *testing.T) {
	const doc = `{
	  "id": "code@2",
	  "parts": {"code": {
	    "entities": [{"name": "file"}, {"name": "module"}],
	    "relations": [
	      {"name": "pairs_with", "from": ["file"], "to": ["module"], "both_ways": true},
	      {"name": "contains", "from": ["file"], "to": ["module"]}
	    ]}}}`
	v, err := load(t, doc).Vocabulary(ontology.PartCode)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := v.AllowsRelation("pairs_with", "module", "file"); !ok {
		t.Errorf("AllowsRelation(pairs_with, module, file) = false (%s); the ontology declared it runs both ways", why)
	}
	// The one-way neighbour is untouched: the swap is licensed by the
	// declaration, not by the shape of the ends.
	ok, why := v.AllowsRelation("contains", "module", "file")
	if ok {
		t.Fatal("AllowsRelation(contains, module, file) = true; nothing said contains runs both ways")
	}
	if !strings.Contains(why, "file") || !strings.Contains(why, "module") {
		t.Errorf("reason = %q, want it to name the ends it does run between", why)
	}
}

// An endpoint that is not a declared entity type at all is still reported
// before the direction, both-ways or not: it is a different fault with a
// different fix, and "runs the wrong way" would point at the edge instead of at
// its end.
func TestABothWaysTypeStillRejectsAnUndeclaredEnd(t *testing.T) {
	v := codeVocab(t)
	ok, why := v.AllowsRelation("imports", "file", "Cluster")
	if ok {
		t.Fatal("AllowsRelation(imports, file, Cluster) = true; Cluster is not an entity type here")
	}
	if !strings.Contains(why, "Cluster") {
		t.Errorf("reason = %q, want it to name the undeclared end", why)
	}
}

// The prompt is the other half of §5b's third mechanism, and it currently tells
// the model a relation runs "in that direction and never the reverse". For a
// both-ways type that sentence is false, and a false constraint is worse than a
// missing one: the model drops the second half of every mutual pair and the
// graph disagrees with the source with nothing to show for it.
func TestPromptSaysWhenARelationRunsBothWays(t *testing.T) {
	got := codeVocab(t).Prompt()
	if !strings.Contains(got, "imports: file -> file (either direction)") {
		t.Errorf("prompt does not mark the both-ways relation:\n%s", got)
	}
	if !strings.Contains(got, "(either direction)") || !strings.Contains(got, "both ways may be true at once") {
		t.Errorf("prompt does not say what the mark means:\n%s", got)
	}
	// And it must not turn into a licence to invent the reverse of an edge the
	// text never stated: "both ways may be true" is not "one way implies the
	// other", and an extractor that materialised the reverse would be writing
	// edges no source ever asserted (§5b).
	if !strings.Contains(got, "extract only the direction the text states") {
		t.Errorf("prompt lets the model invent the reverse edge:\n%s", got)
	}
	// The one-way neighbour keeps the unqualified instruction.
	if !strings.Contains(got, "contains: file -> class") || strings.Contains(got, "contains: file -> class (either direction)") {
		t.Errorf("prompt marked a one-way relation:\n%s", got)
	}
}

// A vocabulary with no both-ways relation renders exactly the prompt it always
// did, byte for byte. The exception sentence appears only when there is an
// exception: a clause offered where nothing needs it is an invitation to look
// for permission, and it would change what comes back from every extraction
// already in production.
func TestPromptIsUnchangedWhenNothingRunsBothWays(t *testing.T) {
	prose, err := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatal(err)
	}
	got := prose.Prompt()
	if strings.Contains(got, "either direction") {
		t.Errorf("prompt names an exception no relation here claims:\n%s", got)
	}
	if !strings.Contains(got, "between; extract it in that direction and never the reverse:\n") {
		t.Errorf("the unqualified instruction changed for a vocabulary with nothing to qualify:\n%s", got)
	}
}
