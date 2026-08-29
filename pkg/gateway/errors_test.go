package gateway_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// The status code is the only part of an error most HTTP clients read, so the
// mapping is a table for the same reason pkg/service's is — and it is checked
// against that one, case for case, because the gateway's whole job is to carry
// those decisions across without reinterpreting them.
//
// The four that matter are four different instructions to a client:
//
//	404 stop asking
//	400 fix the request; retrying is pointless
//	412 the request is fine, the job is not ready
//	429 §8.4's "try later", and the one to retry on
//
// 412 rather than 400 for FAILED_PRECONDITION is the one place this table
// departs from grpc-gateway's defaults, and it is a deliberate departure.
// pkg/service separates InvalidArgument from FailedPrecondition precisely so a
// caller can tell "this can never work" from "this will work in a minute";
// mapping both onto 400 would throw that distinction away at the last step and
// leave every retry loop wrong half the time.
func TestGRPCCodesArriveAsTheRightHTTPStatuses(t *testing.T) {
	cases := []struct {
		name string
		want int
		// wantCode is the gRPC code in the response body. It is asserted as
		// well as the status because a client that reads the body is entitled
		// to the same answer as one that reads the status line.
		wantCode float64
		call     func(t *testing.T) *http.Response
	}{
		{
			name: "a job that never existed", want: http.StatusNotFound, wantCode: 5,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{})
				return f.do(t, http.MethodGet, "/v1/jobs/nope", testToken, nil)
			},
		},
		{
			name: "the result of a job that never existed", want: http.StatusNotFound, wantCode: 5,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{})
				return f.do(t, http.MethodGet, "/v1/jobs/nope/result", testToken, nil)
			},
		},
		{
			name: "deleting a job that never existed", want: http.StatusNotFound, wantCode: 5,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{})
				return f.do(t, http.MethodDelete, "/v1/jobs/nope", testToken, nil)
			},
		},
		{
			name: "a job with no sources", want: http.StatusBadRequest, wantCode: 3,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{})
				return f.do(t, http.MethodPost, "/v1/jobs", testToken, strings.NewReader(`{"ontology":"crm"}`))
			},
		},
		{
			// §5: a document source requires an ontology. There is no
			// unconstrained mode, so no retry of this can ever succeed.
			name: "a document with no ontology", want: http.StatusBadRequest, wantCode: 3,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{})
				src := f.uploadOverGRPC(t, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
				return f.do(t, http.MethodPost, "/v1/jobs", testToken,
					strings.NewReader(`{"source_ids":["`+src+`"]}`))
			},
		},
		{
			// §7.3: a conflict holds the job for a person, and a held job has
			// no result. The request is fine and will be correct once somebody
			// decides, which is what separates this from the 400s above.
			name: "the result of a job held for a person", want: http.StatusPreconditionFailed, wantCode: 9,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{run: func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
					return disputed(), nil
				}})
				src := f.uploadOverGRPC(t, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
				id := f.createOverGRPC(t, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
				f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)
				return f.do(t, http.MethodGet, "/v1/jobs/"+id+"/result", testToken, nil)
			},
		},
		{
			// §8.4: a big result is not one message. HTTP has no 4MB limit, and
			// the refusal is carried across anyway — a gateway that quietly
			// served what gRPC refused would be a second source of truth about
			// what the service does.
			name: "a result too large for one message", want: http.StatusPreconditionFailed, wantCode: 9,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{
					maxResultBytes: 1,
					run: func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
						return alchemy.Result{Entities: []alchemy.Entity{{ID: "a", Type: "Customer", Name: "Acme"}}}, nil
					},
				})
				id := f.aDDLJob(t)
				f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)
				return f.do(t, http.MethodGet, "/v1/jobs/"+id+"/result", testToken, nil)
			},
		},
		{
			// §8.4's admission control. This is the one refusal a retry loop
			// should act on, so it must be tellable from the 400s by status
			// alone.
			name: "a job beyond the declared capacity", want: http.StatusTooManyRequests, wantCode: 8,
			call: func(t *testing.T) *http.Response {
				hold := make(chan struct{})
				t.Cleanup(func() { close(hold) })
				f := serve(t, harness{
					store: job.New(job.Config{Capacity: 1}),
					run: func(ctx context.Context, _ string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
						select {
						case <-hold:
						case <-ctx.Done():
						}
						return alchemy.Result{}, nil
					},
				})
				src := f.uploadOverGRPC(t, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
				f.createOverGRPC(t, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
				return f.do(t, http.MethodPost, "/v1/jobs", testToken,
					strings.NewReader(`{"source_ids":["`+src+`"],"ontology":"crm"}`))
			},
		},
		{
			name: "no credential", want: http.StatusUnauthorized, wantCode: 16,
			call: func(t *testing.T) *http.Response {
				f := serve(t, harness{})
				return f.do(t, http.MethodGet, "/v1/jobs/nope", "", nil)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := c.call(t)
			if resp.StatusCode != c.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.want)
			}
			got := body(t, resp)
			if got["code"] != c.wantCode {
				t.Errorf("body code = %v, want %v", got["code"], c.wantCode)
			}
			if msg, _ := got["message"].(string); strings.TrimSpace(msg) == "" {
				t.Errorf("the refusal carries no message; a status with no sentence is a caller guessing")
			}
		})
	}
}

// §8.4's refusal has to be actionable, not merely correct. A retry loop reads
// the status; a human reads the sentence; and the sentence for the too-large
// result has to name the route that works, because the alternative is a buyer
// concluding the product cannot return their graph.
func TestTheTooLargeRefusalNamesTheStreamingRoute(t *testing.T) {
	f := serve(t, harness{
		maxResultBytes: 1,
		run: func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
			return alchemy.Result{Entities: []alchemy.Entity{{ID: "a", Type: "Customer", Name: "Acme"}}}, nil
		},
	})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	resp := f.do(t, http.MethodGet, "/v1/jobs/"+id+"/result", testToken, nil)
	msg, _ := body(t, resp)["message"].(string)
	if !strings.Contains(msg, "StreamResult") {
		t.Errorf("message = %q, which does not name the RPC that works", msg)
	}
}
