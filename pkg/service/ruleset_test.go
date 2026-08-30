package service_test

import (
	"context"
	"io"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// The policy one chunk of a job was asked under, and a record that names it.
// It is the shape a pipeline produces: the record carries a name, and the
// result carries the set that name resolves to, once.
const policyName = "3f2a9c1d4b607e85"

func extractedUnderAPolicy() alchemy.Result {
	prov := alchemy.Provenance{
		Source: "manual.md", Chunk: 1, Producer: alchemy.ProducerLLMExtract,
		Model: "gpt-x", Ontology: "crm", Chunking: "heading", Confidence: 0.9,
		RuleSet: policyName,
		RuledBy: "authored:violation/unknown_entity_type/type=Flag/producer=llm-extract",
	}
	return alchemy.Result{
		Entities: []alchemy.Entity{{ID: "e1", Type: "Customer", Name: "Acme", Provenance: prov}},
		Counts:   alchemy.Counts{Entities: 1},
		RuleSets: []alchemy.RuleSet{{
			Name: policyName,
			Rules: []alchemy.StandingRule{{
				Name: "authored:violation/unknown_entity_type/type=Flag/producer=llm-extract",
				Told: "--verbose is a switch, not an entity; do not propose these at all; declared by ana@example.com",
			}},
		}},
	}
}

// A record names the policy it was extracted under, and the result it came in
// says what that policy was. Both halves have to cross the wire or the name is
// a pointer into nothing: §5b's promise is that a graph explains itself, and a
// graph that explains itself only to whoever also has the operator's rule file
// does not.
func TestAResultCarriesThePolicyItsRecordsWereExtractedUnder(t *testing.T) {
	cli := dial(t, harness{run: staticResult(extractedUnderAPolicy())})
	src := upload(t, cli, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	res, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(res.GetEntities()) != 1 {
		t.Fatalf("entities = %d, want 1", len(res.GetEntities()))
	}
	prov := res.GetEntities()[0].GetProvenance()
	if prov.GetRuleSet() != policyName {
		t.Errorf("provenance names the rule set %q, want %q", prov.GetRuleSet(), policyName)
	}
	// In force and acted are different claims and the wire has to keep both:
	// which rules were in the room, and which one moved this record.
	if prov.GetRuledBy() == "" {
		t.Error("provenance says no rule acted on a record a rule acted on; a suppression nobody can attribute is what Rule.Covers warns about")
	}
	sets := res.GetRuleSets()
	if len(sets) != 1 || sets[0].GetName() != policyName {
		t.Fatalf("rule sets = %+v, want the one the record names", sets)
	}
	if len(sets[0].GetRules()) != 1 {
		t.Fatalf("the set carries %d rules, want the one that was in force", len(sets[0].GetRules()))
	}
	rule := sets[0].GetRules()[0]
	if rule.GetName() != prov.GetRuledBy() {
		t.Errorf("the set names the rule %q and the record was ruled by %q; a reader cannot join them", rule.GetName(), prov.GetRuledBy())
	}
	if rule.GetTold() == "" {
		t.Error("the set does not say what the model was told; a shape says which class was suppressed and only the sentence says on whose word")
	}
	// The other field is untouched: what this job's review *produced* is a
	// different claim from what it was run under, and a caller feeding one
	// back as the other would re-declare somebody else's policy as their own
	// job's finding.
	if len(res.GetRules()) != 0 {
		t.Errorf("rules = %+v, want none: this job's review decided nothing", res.GetRules())
	}
}

// §8.4's paged result is the one that most needs this. It exists because the
// graph does not fit in one message, which is exactly the volume at which a
// policy repeated on every record stopped being affordable — so the policy
// rides once, on the first page, with the rest of the summary.
func TestAPagedResultCarriesThePolicyOnItsFirstPageOnly(t *testing.T) {
	want := extractedUnderAPolicy()
	want.Entities = graph(3000).Entities
	for i := range want.Entities {
		want.Entities[i].Provenance.RuleSet = policyName
	}
	cli := dial(t, harness{run: staticResult(want), pageSize: 500})
	src := upload(t, cli, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	stream, err := cli.StreamResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("StreamResult: %v", err)
	}
	for pages := 0; ; pages++ {
		page, err := stream.Recv()
		if err == io.EOF {
			if pages < 2 {
				t.Fatalf("the result arrived in %d page(s); this test needs a paged one to prove anything", pages)
			}
			return
		}
		if err != nil {
			t.Fatalf("Recv after %d pages: %v", pages, err)
		}
		switch {
		case pages == 0 && len(page.GetRuleSets()) != 1:
			t.Fatalf("the first page carries %d rule sets, want the one its records name; a reader deciding whether to trust the graph is deciding on what the model was told", len(page.GetRuleSets()))
		case pages > 0 && len(page.GetRuleSets()) != 0:
			t.Fatalf("page %d repeats the policy; carrying it per page is the same mistake as carrying it per record, one level up", pages)
		}
		for _, e := range page.GetEntities() {
			if e.GetProvenance().GetRuleSet() != policyName {
				t.Fatalf("an entity on page %d names the rule set %q, want %q", pages, e.GetProvenance().GetRuleSet(), policyName)
			}
		}
	}
}
