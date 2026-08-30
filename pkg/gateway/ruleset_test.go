package gateway_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// underPolicy is a finished job whose one record was extracted under a policy:
// the record names the set, and the result carries it once.
func underPolicy() alchemy.Result {
	const name = "3f2a9c1d4b607e85"
	return alchemy.Result{
		Entities: []alchemy.Entity{{
			ID: "e1", Type: "Customer", Name: "Acme",
			Provenance: alchemy.Provenance{
				Source: "manual.md", Producer: alchemy.ProducerLLMExtract, Model: "gpt-x",
				RuleSet: name,
				RuledBy: "authored:violation/unknown_entity_type/type=Flag/producer=llm-extract",
			},
		}},
		Counts: alchemy.Counts{Entities: 1},
		RuleSets: []alchemy.RuleSet{{
			Name: name,
			Rules: []alchemy.StandingRule{{
				Name: "authored:violation/unknown_entity_type/type=Flag/producer=llm-extract",
				Told: "--verbose is a switch, not an entity; do not propose these at all; declared by ana@example.com",
			}},
		}},
	}
}

// The buyer curling a result gets the policy with it, under the names the
// document uses. §6's gateway is a translation and never a second source of
// truth, so the fact a record can be resolved back to what the model was told
// has to survive the translation — and it has to survive it in snake_case,
// because a gateway answering "ruleSet" describes a different product than the
// design that sold it.
func TestAResultOverHTTPCarriesThePolicyItsRecordsNameInSnakeCase(t *testing.T) {
	f := serve(t, harness{run: func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
		return underPolicy(), nil
	}})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	resp := f.do(t, http.MethodGet, "/v1/jobs/"+id+"/result", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := body(t, resp)

	sets, ok := got["rule_sets"].([]any)
	if !ok || len(sets) != 1 {
		t.Fatalf("rule_sets = %v (keys %v), want the one policy the records were extracted under", got["rule_sets"], keys(got))
	}
	set := sets[0].(map[string]any)
	rules, ok := set["rules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("the set carries %v, want the rule that was in force", set["rules"])
	}
	rule := rules[0].(map[string]any)
	if rule["told"] == "" || rule["told"] == nil {
		t.Errorf("the set does not say what the model was told: %v", rule)
	}

	entities, _ := got["entities"].([]any)
	if len(entities) != 1 {
		t.Fatalf("entities = %v", got["entities"])
	}
	prov := entities[0].(map[string]any)["provenance"].(map[string]any)
	if prov["rule_set"] != set["name"] {
		t.Errorf("the record names the rule set %v and the result carries %v; a name that resolves to nothing is worse than the repetition it replaced", prov["rule_set"], set["name"])
	}
	if prov["ruled_by"] != rule["name"] {
		t.Errorf("the record was ruled by %v and the set names %v; in force and acted are different claims and both have to cross", prov["ruled_by"], rule["name"])
	}
}
