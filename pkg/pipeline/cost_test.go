package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// §7.2: "the job reports how many model calls it made, by model and stage, in
// the result". By model and stage means one line per pair: four readers and
// three stages report their own spending, and a caller reading the bill should
// not have to add it up.
func TestModelCallsAreOneLinePerModelAndStage(t *testing.T) {
	res, err := Run(context.Background(), mixedJob(t), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	type key struct{ model, stage string }
	seen := map[key]alchemy.ModelCall{}
	for _, c := range res.ModelCalls {
		if _, dup := seen[key{c.Model, c.Stage}]; dup {
			t.Errorf("ModelCalls has two lines for %s/%s: %+v", c.Model, c.Stage, res.ModelCalls)
		}
		seen[key{c.Model, c.Stage}] = c
	}
	extract := seen[key{"gemini-3.6-flash-high", "extract"}]
	// Two chunks were sent and one of them was refused. Both were bought.
	if extract.Calls != 2 {
		t.Errorf("extract calls = %d, want 2 including the chunk that failed: %+v", extract.Calls, res.ModelCalls)
	}
	if extract.Tokens == 0 {
		t.Errorf("extract reported no tokens though the fake reports usage: %+v", res.ModelCalls)
	}
	if got := seen[key{"fake-embed-3", "embed"}]; got.Calls == 0 {
		t.Errorf("the embedding stage is missing from the cost report: %+v", res.ModelCalls)
	}
}

// §7.2's promise is least dispensable on the run that spent the most and got
// the least. A job whose extraction failed outright still reports what the
// failure cost — otherwise an expensive retry looks free.
func TestAFailedStageStillReportsWhatItSpent(t *testing.T) {
	llm := &scriptLLM{name: "gemini-3.6-flash-high", tokens: 7, errs: map[string]error{
		"SuperAI": fmt.Errorf("the endpoint is down"),
	}}
	req := regionRequest(t, doc("architecture.md", "# SuperAI\n\nSuperAI is the cluster in eu-west.\n"))
	req.Models.LLM = llm
	res, err := Run(context.Background(), req, nil)
	if err == nil {
		t.Fatal("Run: want the failure of an extraction that produced nothing")
	}
	if len(res.Entities) != 0 {
		t.Errorf("a failed run returned a graph: %+v", res.Entities)
	}
	if len(res.ModelCalls) != 1 || res.ModelCalls[0].Calls != 1 || res.ModelCalls[0].Stage != "extract" {
		t.Fatalf("ModelCalls = %+v, want the one call the failed stage bought", res.ModelCalls)
	}
	// Tokens is 0 and that is not a gap: a call that failed came back with no
	// usage, which is exactly what alchemy.ModelCall documents 0 to mean —
	// "the provider does not report usage", never "this was free". What must
	// not be 0 is Calls, and it is not.
}
