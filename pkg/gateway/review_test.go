package gateway_test

import (
	"net/http"
	"strings"
	"testing"
)

// §6 chose gRPC because "review is a conversation, not a poll". The gateway's
// answer to Review is therefore a refusal, and these are the three things that
// make a refusal useful rather than merely correct: the right status, a
// sentence naming what to use instead, and the same answers as every other
// route to the callers who were never going to get that far.
func TestReviewIsRefusedRatherThanFaked(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)

	resp := f.do(t, http.MethodPost, "/v1/jobs/"+id+"/review", testToken, strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; a bidirectional stream that answers 200 over HTTP is a deadlock dressed as an endpoint", resp.StatusCode)
	}
	msg, _ := body(t, resp)["message"].(string)
	if !strings.Contains(msg, "alchemy.v1.Alchemy/Review") {
		t.Errorf("message = %q, which does not name the gRPC method that works", msg)
	}
	if !strings.Contains(msg, "bidirectional") {
		t.Errorf("message = %q, which does not say why", msg)
	}
}

// The refusal must not become an oracle. It answers about a job, so it has to
// answer about a job that does not exist the same way GetJob does — otherwise
// an anonymous or unauthorised caller could use the one route the gateway
// answers itself to enumerate a buyer's job IDs.
func TestTheReviewRefusalDoesNotLeakWhichJobsExist(t *testing.T) {
	f := serve(t, harness{})

	resp := f.do(t, http.MethodPost, "/v1/jobs/never-existed/review", testToken, strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d for an unknown job, want 404; a 501 here would confirm that every ID is real", resp.StatusCode)
	}
}
