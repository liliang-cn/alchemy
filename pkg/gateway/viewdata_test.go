package gateway_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/gateway"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// graphOf fetches the JSON the page draws, and insists on 200. The tests that
// want a refusal ask for the status themselves.
func (f *fixture) graphOf(t *testing.T, id string) map[string]any {
	t.Helper()
	resp := f.do(t, http.MethodGet, gateway.ViewPrefix+"jobs/"+id+"/graph", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return body(t, resp)
}

// §5b calls producer "the field that matters", and a viewer that dropped it
// would be a viewer for a different product: the whole claim is that a person
// can see which half of the graph was guessed. So it is asserted on the wire,
// on every record, before anything is drawn.
func TestTheGraphJSONNamesTheProducerOfEveryRecord(t *testing.T) {
	f := serve(t, harness{run: resultOf(mixedProducers())})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	got := f.graphOf(t, id)
	result, _ := got["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result in %v", keys(got))
	}
	for _, field := range []string{"entities", "relations"} {
		records, _ := result[field].([]any)
		if len(records) == 0 {
			t.Fatalf("no %s in the graph JSON", field)
		}
		for i, rec := range records {
			prov, _ := rec.(map[string]any)["provenance"].(map[string]any)
			if prov == nil {
				t.Fatalf("%s[%d] carries no provenance", field, i)
			}
			if prov["producer"] == nil || prov["producer"] == "" {
				t.Errorf("%s[%d] provenance names no producer; §5b's field that matters is missing", field, i)
			}
		}
	}
}

// §5: "a run with 1180 edges and 400 violations is a failure that looks like a
// success". The counts block is therefore not optional and not derived — it is
// the service's own, carried through untouched, including the zeroes.
func TestTheGraphJSONCarriesTheWholeCountsBlock(t *testing.T) {
	f := serve(t, harness{run: resultOf(mixedProducers())})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	result, _ := f.graphOf(t, id)["result"].(map[string]any)
	counts, _ := result["counts"].(map[string]any)
	if counts == nil {
		t.Fatalf("no counts block in %v", keys(result))
	}
	for _, name := range []string{
		"entities", "relations", "deterministic", "inferred",
		"violations", "conflicts", "guesses", "duplicates", "chunks_empty",
	} {
		if _, ok := counts[name]; !ok {
			t.Errorf("counts has no %q; a reader cannot tell none from not measured", name)
		}
	}
}

// §7.3 holds a job on a conflict and pkg/service refuses its result on
// purpose. A person asked to resolve that conflict is exactly the person who
// needs to look, so the view answers — with what the service is willing to say
// about a held job, which is WatchJob's counts and every conflict it found.
//
// The three things this asserts are the three that make a held view honest: it
// is marked held, it carries the conflicts with both sides, and it carries no
// graph. A held graph must be impossible to mistake for an accepted one, and
// the strongest available guarantee of that is that there is no graph in it.
func TestAHeldJobIsShownAsHeldAndCarriesNoGraph(t *testing.T) {
	f := serve(t, harness{run: resultOf(disputed())})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	got := f.graphOf(t, id)
	if got["held"] != true {
		t.Errorf("held = %v, want true; a held graph that does not say so is the confident wrong answer this design exists to prevent", got["held"])
	}
	because, _ := got["because"].(string)
	if !strings.Contains(because, "conflict") {
		t.Errorf("because = %q, which does not say why the job is held", because)
	}
	result, _ := got["result"].(map[string]any)
	if n := len(result["entities"].([]any)); n != 0 {
		t.Errorf("%d entities on a held job; GetResult refuses a held graph and the view must not go round it", n)
	}
	conflicts, _ := result["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("%d conflicts, want 1; the one thing a held job is waiting for is the one thing it must show", len(conflicts))
	}
	c, _ := conflicts[0].(map[string]any)
	for _, side := range []string{"left", "right"} {
		claim, _ := c[side].(map[string]any)
		if claim == nil {
			t.Fatalf("conflict has no %s claim", side)
		}
		prov, _ := claim["provenance"].(map[string]any)
		if prov == nil || prov["source"] == "" {
			t.Errorf("the %s claim names no source; a reviewer deciding between two sources needs to see both", side)
		}
	}
}

// §8.4: a large result is paged because it does not fit one message. A
// 200,000-node graph in a browser is not a view, it is a hang — so the view
// stops reading at a budget and says, in the JSON and therefore on the page,
// that it stopped.
//
// What must survive the sampling is the numbers. The counts block describes
// the whole graph, not the sample: a viewer that showed 1,200 of 400,000 edges
// and reported 1,200 would be the failure that looks like a success, one level
// up.
func TestAGraphTooLargeToDrawIsSampledVisibly(t *testing.T) {
	big := crowd(gateway.ViewMaxNodes + 500)
	f := serve(t, harness{run: resultOf(big)})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	got := f.graphOf(t, id)
	if got["truncated"] != true {
		t.Errorf("truncated = %v for a graph of %d entities, want true", got["truncated"], len(big.Entities))
	}
	shown, _ := got["shown"].(map[string]any)
	if shown == nil {
		t.Fatal("no shown block; sampling that does not say how much it showed is sampling in silence")
	}
	drawn := int(shown["entities"].(float64))
	if drawn > gateway.ViewMaxNodes {
		t.Errorf("drew %d entities, over the %d the view will draw", drawn, gateway.ViewMaxNodes)
	}
	if drawn == 0 {
		t.Error("drew nothing; the refusal is meant to be a smaller picture, not an empty one")
	}
	result, _ := got["result"].(map[string]any)
	counts, _ := result["counts"].(map[string]any)
	if whole := int(counts["entities"].(float64)); whole != len(big.Entities) {
		t.Errorf("counts.entities = %d, want %d; the numbers describe the graph and never the sample", whole, len(big.Entities))
	}
}

// A job with no result yet is not a held job and must not read like one. The
// service's sentence and its code are passed through: 412 says the request was
// fine and the state was not, which is what a running job is.
func TestAJobWithNoResultYetIsRefusedWithTheServicesOwnSentence(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	f := serve(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return alchemy.Result{}, ctx.Err()
	}})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_RUNNING)

	resp := f.do(t, http.MethodGet, gateway.ViewPrefix+"jobs/"+id+"/graph", testToken, nil)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d for a running job, want 412", resp.StatusCode)
	}
}

// The view must not become an oracle either. The same reasoning as
// TestTheReviewRefusalDoesNotLeakWhichJobsExist: these are the routes the
// gateway answers itself, so a job that does not exist has to answer the way
// GetJob does.
func TestTheViewDoesNotLeakWhichJobsExist(t *testing.T) {
	f := serve(t, harness{})
	for _, path := range []string{
		gateway.ViewPrefix + "jobs/never-existed",
		gateway.ViewPrefix + "jobs/never-existed/graph",
	} {
		resp := f.do(t, http.MethodGet, path, testToken, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

// resultOf turns a fixed Result into a Runner.
//
// It publishes a conflict event per conflict before returning, which is not
// test decoration: §7.3 requires that "WatchJob emits a conflict when it is
// found, not at the end", pkg/pipeline does exactly that, and the held view
// reads those events. A fake runner that returned conflicts in its Result and
// announced none would be a fake that does not keep the contract the thing it
// stands for keeps, and the test would then be asserting something no real run
// produces.
func resultOf(res alchemy.Result) func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
	return func(_ context.Context, _ string, _ service.JobSpec, events chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		for i := range res.Conflicts {
			events <- service.Event{Conflict: &res.Conflicts[i], Counts: res.Counts}
		}
		return res, nil
	}
}

// mixedProducers is the smallest graph that has both halves of §5b in it: an
// edge a schema stated and an edge a model proposed.
func mixedProducers() alchemy.Result {
	declared := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	guessed := alchemy.Provenance{
		Source: "architecture.pdf", Chunk: 14, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Confidence: 0.82,
	}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "Table", Name: "orders", Provenance: declared},
			{ID: "e2", Type: "Table", Name: "customer", Provenance: declared},
			{ID: "e3", Type: "System", Name: "SuperAI", Provenance: guessed},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "REFERENCES", Provenance: declared},
			{From: "e3", To: "e1", Type: "USES", Provenance: guessed},
		},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownRelationType, Subject: "e3 -[USES]-> e1",
			Detail: "USES is not a relation type the ontology declares", Provenance: guessed,
		}},
		Counts: alchemy.Counts{
			Entities: 3, Relations: 2, Deterministic: 3, Inferred: 2,
			Violations: 1, ChunksEmpty: 4,
		},
	}
}

// crowd is a graph nobody can look at: n entities in a chain.
func crowd(n int) alchemy.Result {
	prov := alchemy.Provenance{Source: "dump.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	res := alchemy.Result{Counts: alchemy.Counts{Entities: n, Relations: n - 1, Deterministic: n}}
	for i := range n {
		res.Entities = append(res.Entities, alchemy.Entity{
			ID: fmt.Sprintf("e%d", i), Type: "Table", Name: fmt.Sprintf("t%d", i), Provenance: prov,
		})
		if i > 0 {
			res.Relations = append(res.Relations, alchemy.Relation{
				From: fmt.Sprintf("e%d", i-1), To: fmt.Sprintf("e%d", i), Type: "REFERENCES", Provenance: prov,
			})
		}
	}
	return res
}

// A held job's conflicts reach the view over WatchJob, and WatchJob replays
// what was announced while the job ran. §7.3 requires that announcement, and
// pkg/pipeline makes it — but the view must not depend on it being complete.
// If the counts declare more conflicts than the stream produced, the view says
// so, exactly as it does when a graph is too large to draw: the number of
// findings a person is looking at, against the number the job actually has, is
// the difference between "I have seen the problem" and "I have seen some of
// it".
func TestAHeldJobSaysWhenItCouldNotShowEveryConflict(t *testing.T) {
	res := disputed()
	// Two conflicts counted, one announced: a runner that found the second
	// after its last event, or a watcher that attached late.
	res.Counts.Conflicts = 2
	f := serve(t, harness{run: resultOf(res)})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	got := f.graphOf(t, id)
	if got["truncated"] != true {
		t.Errorf("truncated = %v when 1 of 2 conflicts could be shown; a partial view of a held job that does not say it is partial is the whole failure this design is about",
			got["truncated"])
	}
	shown, _ := got["shown"].(map[string]any)
	if n := int(shown["conflicts"].(float64)); n != 1 {
		t.Errorf("shown.conflicts = %d, want 1", n)
	}
	result, _ := got["result"].(map[string]any)
	counts, _ := result["counts"].(map[string]any)
	if n := int(counts["conflicts"].(float64)); n != 2 {
		t.Errorf("counts.conflicts = %d, want 2; the numbers are the job's and never the view's", n)
	}
}
