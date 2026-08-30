package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// underRules resolves a record back to the rules the model had been told,
// exactly the way a reader holding only the Result has to do it: the record
// names a set, and the result says what is in it.
//
// It fails the test when a record names a set the result does not carry. That
// is the failure the whole design turns on — a name nobody can resolve is
// worse than the repetition it replaced, because the repetition at least said
// what it meant.
func underRules(t *testing.T, res alchemy.Result, p alchemy.Provenance) []alchemy.StandingRule {
	t.Helper()
	if p.RuleSet == "" {
		return nil
	}
	for _, set := range res.RuleSets {
		if set.Name == p.RuleSet {
			return set.Rules
		}
	}
	t.Fatalf("a record names the rule set %q, which this result does not carry; the sets it does carry are %+v", p.RuleSet, res.RuleSets)
	return nil
}

// namedBy is whether a resolved set contains a rule of this name.
func namedBy(rules []alchemy.StandingRule, name string) bool {
	for _, r := range rules {
		if r.Name == name {
			return true
		}
	}
	return false
}

// The replacement for what the per-record string used to do, and it has to do
// all of it: a reader holding only the Result must be able to take any record,
// find the policy it was asked under, and read back every rule in it — by
// identity, and in the words the model was actually shown.
func TestARecordNamesARuleSetTheResultCanResolve(t *testing.T) {
	policy := policyOf(t, 3)
	req := regionRequest(t, doc("architecture.md", cleanDoc))
	req.Models.LLM = cleanLLM()
	req.Inbox = Answered(nil, policy)

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Entities) == 0 {
		t.Fatal("nothing was extracted, so this proves nothing")
	}
	for _, e := range res.Entities {
		if e.Provenance.RuleSet == "" {
			t.Fatalf("entity %q was extracted under a three-rule policy and names no rule set", e.ID)
		}
		told := underRules(t, res, e.Provenance)
		if len(told) != len(policy) {
			t.Fatalf("entity %q was extracted under %d rules and its set names %d", e.ID, len(policy), len(told))
		}
		for _, rule := range policy {
			if !namedBy(told, rule.Name()) {
				t.Errorf("the set entity %q names does not contain %q; a reader cannot say which rules the model had been told", e.ID, rule.Name())
			}
		}
		// Identity is not the whole of it. §5c's origin requirement is that a
		// later reader can see why a rule exists, and the sentence the model
		// was shown is the one thing that carries the reason, the author and
		// the correction all at once.
		for _, r := range told {
			if !strings.Contains(r.Told, "ana@example.com") {
				t.Errorf("the set says the model was told %q, without who declared it; a standing answer nobody signed is the unexplainable policy §5c names", r.Told)
			}
		}
	}
}

// Two policies are two different things to have been extracted under, and a
// name that could not tell them apart would be a name for nothing. This is the
// property the per-record string had for free and the one a digest has to earn.
func TestTwoPoliciesAreNamedDifferentlyAndOnePolicyIsNamedTheSameTwice(t *testing.T) {
	run := func(rules []review.Rule) alchemy.Result {
		t.Helper()
		req := regionRequest(t, doc("architecture.md", cleanDoc))
		req.Models.LLM = cleanLLM()
		if len(rules) > 0 {
			req.Inbox = Answered(nil, rules)
		}
		res, err := Run(context.Background(), req, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(res.Entities) == 0 {
			t.Fatal("nothing was extracted, so this proves nothing")
		}
		return res
	}
	nameOf := func(res alchemy.Result) string { return res.Entities[0].Provenance.RuleSet }

	one := policyOf(t, 1)
	two := policyOf(t, 2)

	// Stable: the same policy names the same set on a second run, and — since
	// nothing here is seeded, timed or addressed by a pointer — on another
	// node of a clustered job (§8.3) too. A name that differed between two
	// nodes would make two halves of one job look like two policies.
	if a, b := nameOf(run(one)), nameOf(run(one)); a != b {
		t.Errorf("one policy named %q on one run and %q on the next; a name that is not stable across processes names nothing", a, b)
	}
	// Distinct: adding a rule is a different policy to have been extracted
	// under, and §6's whole point is that the set changes during a run.
	if a, b := nameOf(run(one)), nameOf(run(two)); a == b {
		t.Errorf("a one-rule policy and a two-rule policy are both named %q; two records asked under different rules would be indistinguishable", a)
	}
	// A record asked under no policy at all still says so by saying nothing.
	if got := nameOf(run(nil)); got != "" {
		t.Errorf("a record extracted with nobody's policy in force names the rule set %q", got)
	}
}

// A rule that was merely in force and a rule that actually acted on a record
// are two different claims, and until now neither was distinguishable from the
// other: a graph that came back with a record retyped said which policy was in
// the room and never which line of it moved the record.
//
// Rule.Covers' own comment is the argument — "a queue that is three items
// shorter than the findings should be able to say which rule took each of the
// three away, and who made it" — and a record that survives a rule is the same
// question one step further on: something changed it, and the reader is owed
// the name of what.
func TestARecordSaysWhichRuleActedOnIt(t *testing.T) {
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

	req := regionRequest(t, doc("mixed.md", "# Mixed\n\nW1 is a Widget on node-a.\n"))
	req.Models.LLM = &scriptLLM{replies: map[string]string{
		"W1": `{"entities":[{"type":"Widget","name":"W1"},{"type":"Node","name":"node-a"}],"relations":[]}`,
	}}
	req.Inbox = Answered(nil, []review.Rule{rule})

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byName := map[string]alchemy.Entity{}
	for _, e := range res.Entities {
		byName[e.Name] = e
	}
	acted, ok := byName["W1"]
	if !ok {
		t.Fatalf("entities = %+v, want the one the rule retyped", res.Entities)
	}
	if acted.Type != "Cluster" {
		t.Fatalf("W1 has type %q, so no rule acted on it and this test proves nothing", acted.Type)
	}
	if acted.Provenance.RuledBy != rule.Name() {
		t.Errorf("W1 was retyped by a rule and its provenance says it was ruled by %q, want %q", acted.Provenance.RuledBy, rule.Name())
	}
	// The other half, and it is what stops the field from meaning "there was a
	// policy": a record the same policy said nothing about names no rule.
	untouched, ok := byName["node-a"]
	if !ok {
		t.Fatalf("entities = %+v, want the record no rule was about", res.Entities)
	}
	if untouched.Provenance.RuledBy != "" {
		t.Errorf("node-a says it was ruled by %q, and no rule was ever about it", untouched.Provenance.RuledBy)
	}
	// It was still extracted under the policy, which is the distinction the
	// two fields exist to keep: in force is not the same as acted.
	if untouched.Provenance.RuleSet == "" {
		t.Error("node-a was extracted under a policy and names no rule set; in force and acted are different claims and both are owed")
	}
}

// The set changes *during* a run — that is what §6 chose a stream for — so a
// record cannot simply say "the job's rules". It has to name the subset that
// was in force at the moment its own chunk was asked, and a result that ran
// under two policies has to carry both.
//
// The corpus starts under an operator's standing policy and gains a reviewer's
// rule halfway through, which is §7.3's pipeline in the middle of becoming
// unattended: the first chunk was asked under one policy and the second under
// a strictly larger one, and neither record is allowed to claim the other's.
func TestAPolicyThatGrowsMidRunGivesEachChunkTheSetItWasAskedUnder(t *testing.T) {
	standing := policyOf(t, 1)[0]
	rule, decision := widgetRule(t, twoSourceRequest(t, &watchfulLLM{replies: map[string]string{
		"W1": `{"entities":[{"type":"Widget","name":"W1"}],"relations":[]}`,
		"W2": `{"entities":[{"type":"Widget","name":"W2"}],"relations":[]}`,
	}}))

	in := &liveInbox{rules: []review.Rule{standing}}
	req := twoSourceRequest(t, &watchfulLLM{
		replies: map[string]string{
			"W1": `{"entities":[{"type":"Widget","name":"W1"}],"relations":[]}`,
			"W2": `{"entities":[{"type":"Widget","name":"W2"}],"relations":[]}`,
		},
		after: map[string]func(){"W1": func() { in.says(decision, rule) }},
	})
	req.Inbox = in

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byName := map[string]alchemy.Entity{}
	for _, e := range res.Entities {
		byName[e.Name] = e
	}
	first, second := byName["W1"].Provenance, byName["W2"].Provenance
	if first.RuleSet == "" || second.RuleSet == "" {
		t.Fatalf("W1 names %q and W2 names %q; both were extracted under a policy", first.RuleSet, second.RuleSet)
	}
	if first.RuleSet == second.RuleSet {
		t.Errorf("both chunks name the rule set %q, and a rule was made between them", first.RuleSet)
	}
	if len(res.RuleSets) != 2 {
		t.Fatalf("the result carries %d rule sets, want the two this job ran under: %+v", len(res.RuleSets), res.RuleSets)
	}
	before, after := underRules(t, res, first), underRules(t, res, second)
	if len(before) != 1 || !namedBy(before, standing.Name()) {
		t.Errorf("W1 was asked under %+v, want only the policy that was in force before the reviewer decided", before)
	}
	if len(after) != 2 || !namedBy(after, standing.Name()) || !namedBy(after, rule.Name()) {
		t.Errorf("W2 was asked under %+v, want both the standing policy and the rule made after W1", after)
	}
}
