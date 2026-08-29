package gateway_test

import (
	"net/http"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// The first thing a buyer does is curl a job. §6's reason for the gateway
// existing is exactly this call, so it is the first thing tested.
//
// It asserts the field names as well as the status, because the JSON a buyer
// reads is part of the contract: DESIGN.md writes its own examples in
// snake_case (§5b's counts block), and a gateway that answered "createdAt"
// would be describing a different product than the document that sold it.
func TestAJobCanBeFetchedOverHTTP(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	resp := f.do(t, http.MethodGet, "/v1/jobs/"+id, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := body(t, resp)
	if got["id"] != id {
		t.Errorf("id = %v, want %q (keys %v)", got["id"], id, keys(got))
	}
	if got["state"] != "JOB_STATE_SUCCEEDED" {
		t.Errorf("state = %v, want JOB_STATE_SUCCEEDED", got["state"])
	}
	if _, ok := got["created_at"]; !ok {
		t.Errorf("no created_at in %v; the document's own JSON is snake_case", keys(got))
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
