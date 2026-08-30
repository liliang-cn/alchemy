package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/review"
)

// flagDoc is DESIGN.md §6's own sentence as a corpus: "an extractor that has
// already learned 'this is not an entity' should stop proposing it". The
// manual is full of command-line switches and the model keeps typing them as
// entities the ontology never declared.
const flagDoc = "# Options\n\n--verbose is a Flag.\n"

func flagLLM() *scriptLLM {
	return &scriptLLM{replies: map[string]string{
		"--verbose": `{"entities":[{"type":"Flag","name":"--verbose"}],"relations":[]}`,
	}}
}

// authoredFlagRule is a person writing policy before anything has run: no job
// has been held, no queue has been offered, and nobody has seen this mistake
// in this service. §5c's origin requirement is met by the author, the stated
// reason and the date rather than by a decision on an item that never existed.
func authoredFlagRule(t *testing.T) review.Rule {
	t.Helper()
	rule, err := review.Authorship{
		Shape:   "violation/unknown_entity_type/type=Flag/producer=llm-extract/model=fake-llm",
		Verb:    review.VerbReject,
		By:      "ana@example.com",
		Because: "--verbose is a command-line switch, not an entity; this manual is full of them",
		At:      time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
	}.Rule()
	if err != nil {
		t.Fatalf("Authorship.Rule: %v", err)
	}
	return rule
}

// THE test this capability exists for. A rule a person wrote up front changes
// what a first-ever import extracts — no job ran before it, no queue was ever
// offered, and nothing was held. Today a rule can only exist after the mistake
// it prevents has already happened once; an operator who knows their corpus
// cannot state policy in advance, and a nightly pipeline cannot carry one as
// configuration.
func TestAnAuthoredRuleChangesWhatAFirstEverImportExtracts(t *testing.T) {
	// What the same corpus does with nobody's policy in front of it: the model
	// proposes the switch as an entity and the graph carries it, with a
	// violation to say the ontology never declared it.
	bare := regionRequest(t, doc("options.md", flagDoc))
	bare.Models.LLM = flagLLM()
	before, err := Run(context.Background(), bare, nil)
	if err != nil {
		t.Fatalf("Run(no rules): %v", err)
	}
	if len(before.Entities) != 1 || before.Entities[0].Type != "Flag" {
		t.Fatalf("entities = %+v, want the Flag this rule is about; the test proves nothing otherwise", before.Entities)
	}
	if len(before.Violations) != 1 {
		t.Fatalf("violations = %+v, want the one the ontology raises for Flag", before.Violations)
	}

	rule := authoredFlagRule(t)
	req := regionRequest(t, doc("options.md", flagDoc))
	req.Models.LLM = flagLLM()
	// Not review mode, and no conversation: this is the unattended nightly
	// import of §7.3, carrying its policy as configuration.
	req.Inbox = Answered(nil, []review.Rule{rule})

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run(authored rule): %v", err)
	}
	if len(res.Entities) != 0 {
		t.Errorf("entities = %+v, want the switch dropped by the rule that was written before the job ran", res.Entities)
	}
	// The record never reached the verifier, so there is no violation to
	// report either: the rule settled the proposal at the chunk that made it.
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v, want none: the record the violation was about never entered the graph", res.Violations)
	}
	// §5: the numbers needed to distrust the graph. A record a rule removed
	// without anybody being asked about it is exactly the number a caller
	// needs, and a graph that simply came back one entity shorter would make
	// §5's obligation quietly false.
	if res.Counts.Dropped != 1 {
		t.Errorf("counts.dropped = %d, want 1: a record removed by a rule is a record the caller can count", res.Counts.Dropped)
	}
}

// The other half of §6's sentence: the model is told, so it stops proposing
// what a person has already ruled out, rather than only having its answer
// filtered afterwards.
func TestAnAuthoredRuleIsInTheFirstPromptTheModelSees(t *testing.T) {
	llm := &watchfulLLM{replies: map[string]string{
		"--verbose": `{"entities":[{"type":"Flag","name":"--verbose"}],"relations":[]}`,
	}}
	req := regionRequest(t, doc("options.md", flagDoc))
	req.Models.LLM = llm
	req.Inbox = Answered(nil, []review.Rule{authoredFlagRule(t)})

	if _, err := Run(context.Background(), req, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := llm.toldAbout("command-line switch"); got != 1 {
		t.Errorf("the authored reason was in %d system prompts, want the one chunk this corpus has", got)
	}
	if got := llm.toldAbout("ana@example.com"); got != 1 {
		t.Errorf("the model was told the policy %d times without who declared it; a standing answer nobody signed is the unexplainable policy §5c names", got)
	}
}

// §5b: a graph explains itself, and a reader must be able to tell the two
// claims apart. "A person looked at this exact finding and generalised" and "a
// person declared this in advance" are different warrants for the same
// suppression, and provenance that rendered them identically would let the
// weaker one be read as the stronger.
func TestProvenanceSaysWhetherARuleWasAuthoredOrReviewed(t *testing.T) {
	rule, err := review.Authorship{
		Shape:   "violation/unknown_entity_type/type=Widget/producer=llm-extract/model=fake-llm",
		Verb:    review.VerbAlways,
		By:      "ana@example.com",
		Because: "these are clusters written up by someone who calls them widgets",
		At:      time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		Edit:    &review.Edit{Type: "Cluster"},
	}.Rule()
	if err != nil {
		t.Fatalf("Authorship.Rule: %v", err)
	}
	req := regionRequest(t, doc("first.md", firstSection))
	req.Models.LLM = &scriptLLM{replies: map[string]string{
		"W1": `{"entities":[{"type":"Widget","name":"W1"}],"relations":[]}`,
	}}
	req.Inbox = Answered(nil, []review.Rule{rule})

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Entities) != 1 {
		t.Fatalf("entities = %+v, want the one the rule retyped", res.Entities)
	}
	// The record names the policy; the result says what was in it. Both
	// halves of the claim are still checked — which rule, and on whose
	// warrant — they are now read off the set the record points at rather
	// than off a copy of the set carried by every record.
	told := underRules(t, res, res.Entities[0].Provenance)
	if !namedBy(told, rule.Name()) {
		t.Fatalf("the rule set the record names is %+v, want it to contain the rule the chunk was extracted under, %q", told, rule.Name())
	}
	if !strings.HasPrefix(rule.Name(), string(review.OriginAuthored)+":") {
		t.Errorf("the rule is named %q, want it to say this rule was declared in advance rather than decided on a finding", rule.Name())
	}
}
