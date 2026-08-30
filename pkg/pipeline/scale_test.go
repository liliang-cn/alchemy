package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// A corpus that produces records and no questions: two entities and the edge
// between them, all of it inside the vocabulary. Nothing here is violated,
// nothing is dropped, and nothing is retyped — so what a record's provenance
// weighs is a fact about the policy in force and about nothing else.
const cleanDoc = "# SuperAI\n\nSuperAI is the cluster in eu-west, on node-a.\n"

func cleanLLM() *scriptLLM {
	return &scriptLLM{replies: map[string]string{
		"eu-west": `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],
		             "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
	}}
}

// policyOf is an operator's rule file with n rules in it, of the size §8 is
// about: a nightly pipeline carrying policy as configuration (§7.3). Every
// rule is in force and none of them matches anything this corpus produces,
// which is the ordinary case — a policy is written for a corpus over months,
// and most of it says nothing about tonight's import.
func policyOf(t *testing.T, n int) []review.Rule {
	t.Helper()
	out := make([]review.Rule, 0, n)
	for i := range n {
		rule, err := review.Authorship{
			Shape:   fmt.Sprintf("violation/unknown_entity_type/type=Legacy%02d/producer=llm-extract/model=fake-llm", i),
			Verb:    review.VerbReject,
			By:      "ana@example.com",
			Because: fmt.Sprintf("Legacy%02d is a type this corpus stopped using in 2019", i),
			At:      time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		}.Rule()
		if err != nil {
			t.Fatalf("Authorship.Rule: %v", err)
		}
		out = append(out, rule)
	}
	return out
}

// provenanceBytes is what one record's provenance costs on the wire, measured
// rather than reasoned about.
//
// It is measured over the whole struct, by field-name-independent encoding, so
// that the assertion below survives the fields being renamed or rearranged.
// The question it answers is the only one that matters at §8's volume: how
// many bytes does carrying this policy add to every record in the graph.
func provenanceBytes(t *testing.T, p alchemy.Provenance) int {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshalling provenance: %v", err)
	}
	return len(b)
}

// perRecordProvenance runs the corpus under a policy and returns the mean
// provenance size of the records it produced.
func perRecordProvenance(t *testing.T, rules []review.Rule) int {
	t.Helper()
	req := regionRequest(t, doc("architecture.md", cleanDoc))
	req.Models.LLM = cleanLLM()
	if len(rules) > 0 {
		req.Inbox = Answered(nil, rules)
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run under %d rules: %v", len(rules), err)
	}
	total, n := 0, 0
	for _, e := range res.Entities {
		total += provenanceBytes(t, e.Provenance)
		n++
	}
	for _, rel := range res.Relations {
		total += provenanceBytes(t, rel.Provenance)
		n++
	}
	if n == 0 {
		t.Fatalf("the corpus produced no records under %d rules, so this measures nothing", len(rules))
	}
	return total / n
}

// perRecordSlack is what a record may gain when forty-nine rules are added to
// the policy it runs under. It is small rather than zero because a record is
// allowed to name the policy it was asked under; it is not allowed to carry it.
const perRecordSlack = 8

// THE scale assertion. §8 is about four hundred thousand records in one import
// and §8.4 already says a large result is paged because it does not fit in one
// message; a per-record cost that grows with the size of the operator's rule
// file is the thing that decides whether that result is a graph or a gigabyte
// of repeated policy.
//
// It is written as a measurement of two real runs rather than as an assertion
// about a rendering, because the property is about bytes: whatever the field
// is called and however it is spelled, a run under fifty rules must not put
// fifty rules' worth of anything on every record it produces.
func TestProvenanceDoesNotGrowWithTheNumberOfRulesInForce(t *testing.T) {
	none := perRecordProvenance(t, nil)
	one := perRecordProvenance(t, policyOf(t, 1))
	fifty := perRecordProvenance(t, policyOf(t, 50))
	t.Logf("provenance per record: %d bytes under no rules, %d under 1, %d under 50", none, one, fifty)

	// The property that must survive whatever the fix is: a record extracted
	// under a policy is still distinguishable from one extracted under none.
	if one <= none {
		t.Fatalf("a record extracted under one rule carries %d bytes of provenance and one extracted under no rules carries %d; they must stay distinguishable", one, none)
	}
	if growth := fifty - one; growth > perRecordSlack {
		t.Errorf("a record carries %d bytes of provenance under a 50-rule policy and %d under a 1-rule policy: %d bytes more on every record, which is O(rules) per record. At §8's four hundred thousand records that is %d MB of identical repeated policy",
			fifty, one, growth, growth*400_000>>20)
	}
}
