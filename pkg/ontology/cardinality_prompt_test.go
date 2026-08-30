package ontology_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// TestTheExtractorIsNeverToldHowManyOfAnEdgeAreAllowed pins an omission.
//
// A cardinality is the one thing in the vocabulary that must not reach the
// model. Every other field either names what exists — which is §5's "the same
// list on both sides" — or withholds a contradiction, which is what BothWays
// does. This one would be a rule the model acts on, and the act is to drop the
// second claim: a profile saying Ada is CTO and a correction saying Bruno is
// would come back as one edge, from one chunk, with nothing recording that
// there had been a disagreement at all.
//
// The conflict is the product. §7.3 exists so a person answers it, and an
// extractor that resolved it first would be selling a graph that looks cleaner
// than the corpus it came from — the confident wrong answer with a citation.
//
// It is a test rather than a comment because the prompt is assembled by
// walking the struct, so the next field added to RelationType reaches the model
// by default and this is what asks whether it should.
func TestTheExtractorIsNeverToldHowManyOfAnEdgeAreAllowed(t *testing.T) {
	v := ontology.Vocabulary{
		Entities: []ontology.EntityType{{Name: "Person"}, {Name: "Organization"}},
		Relations: []ontology.RelationType{{
			Name: "CHIEF_TECHNOLOGY_OFFICER_OF",
			From: []string{"Person"}, To: []string{"Organization"},
			AtMostOneIn: true, AtMostOneOut: true,
		}},
	}
	for _, prompt := range []struct{ name, text string }{
		{"Prompt", v.Prompt()},
		{"TablePrompt", v.TablePrompt()},
	} {
		if !strings.Contains(prompt.text, "CHIEF_TECHNOLOGY_OFFICER_OF") {
			t.Fatalf("%s does not name the relation at all; this test's premise is wrong", prompt.name)
		}
		for _, leak := range []string{"at_most_one", "AtMostOne", "at most one", "only one", "exactly one"} {
			if strings.Contains(prompt.text, leak) {
				t.Errorf("%s tells the model %q: an extractor that knows a company has one CTO "+
					"resolves the disagreement itself, in one chunk, and the conflict §7.3 holds "+
					"the job for never reaches anybody\n%s", prompt.name, leak, prompt.text)
			}
		}
	}
}
