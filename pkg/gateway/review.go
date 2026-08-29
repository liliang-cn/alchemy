package gateway

import (
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Review is the one RPC with no honest translation into HTTP, and this file is
// the refusal rather than an attempt.
//
// The reasoning is DESIGN.md §6's, read the other way round. §6 chose gRPC
// because "review is a conversation, not a poll": a reviewer wants items as
// they are found and wants a decision to reach an extraction that has not run
// yet, and "modelled over HTTP it becomes polling plus a submit endpoint,
// which is the same thing with latency and more state on both sides". A
// gateway that generated that endpoint anyway would be re-introducing exactly
// the shape the design rejected, and would be doing it silently — the route
// would answer 200 and a buyer would build against a queue that cannot deliver
// item four while item three is being answered.
//
// grpc-gateway will happily generate a bidirectional handler. Over HTTP/1.1 it
// is worse than useless: the request body must be complete before the response
// can be read, so a reviewer who has not finished sending decisions never
// receives an item, and a reviewer who has finished sending has nothing left
// to decide with. That is a deadlock dressed as an endpoint.
//
// So the path exists and says so. 501 Not Implemented is the accurate code —
// the server does not support the functionality required to fulfil the
// request — and the body names the gRPC method that does. A 404 would have
// been less work and would have told a buyer that Alchemy has no review at
// all, which is false and is the more expensive wrong answer.

// Refusal is a route the gateway answers itself, because the RPC behind it
// cannot be translated.
//
// It is exported so that a test can build its route table from the gateway's
// own declaration rather than from a list somebody maintains — the same
// reasoning that has the rest of that table come from the generated OpenAPI
// document. A second refusal added here is covered by the auth table without
// anybody remembering to add it.
type Refusal struct {
	Method string
	// Path is the grpc-gateway template, so it reads like the generated ones.
	Path string
	// RPC is the method it stands in for, named so the coverage test can see
	// that no RPC is left without an answer.
	RPC string
	// Status is what the route answers an authenticated caller.
	Status int
	// Because is the sentence the caller gets. It names the gRPC method,
	// because a refusal that does not say what to do instead is a dead end.
	Because string
}

// Refusals is every such route. There is one.
func Refusals() []Refusal {
	return []Refusal{{
		Method: "POST",
		Path:   "/v1/jobs/{job_id}/review",
		RPC:    "Review",
		Status: 501,
		Because: "Review is a bidirectional stream and has no honest translation into HTTP: " +
			"a reviewer must be able to answer one item while the next is still arriving, and an HTTP request body " +
			"must be finished before its response can be read. Use the gRPC method alchemy.v1.Alchemy/Review. " +
			"Everything else about a held job — watching it, fetching it, deleting it — works over HTTP; " +
			"only unblocking it does not.",
	}}
}

// refuse answers a Refusal's route.
//
// It does not simply write the status, and the extra step is the point: before
// refusing, it asks the service about the job named in the path, forwarding
// the caller's credential exactly as every generated route does. That is what
// keeps the gateway from holding an opinion it has no right to.
//
// The consequence is that this route answers like GetJob in every way except
// the last one. An anonymous caller gets 401 because the service said so; a
// caller naming a job that does not exist gets 404 because the service said
// so; only a caller the service was willing to answer reaches the 501. A
// gateway that skipped the probe would have to decide for itself what a valid
// token looks like — a second source of truth about authentication, which is
// the one place that phrase is not a design nicety but a vulnerability — or it
// would announce to anonymous callers which of a buyer's job IDs are real.
func refuse(mux *runtime.ServeMux, client alchemyv1.AlchemyClient, ref Refusal) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
		_, outbound := runtime.MarshalerForRequest(mux, r)
		ctx, err := runtime.AnnotateContext(r.Context(), mux, r, probeMethod, runtime.WithHTTPPathPattern(ref.Path))
		if err != nil {
			httpError(ctx, mux, outbound, w, r, err)
			return
		}
		if _, err := client.GetJob(ctx, &alchemyv1.GetJobRequest{JobId: pathParams["job_id"]}); err != nil {
			httpError(ctx, mux, outbound, w, r, err)
			return
		}
		httpError(ctx, mux, outbound, w, r, status.Error(codes.Unimplemented, ref.Because))
	}
}

// probeMethod is the RPC the refusal borrows to ask "may this caller see this
// job at all". It is named as a constant so that the borrowing is visible: the
// gateway is making a call the caller did not ask for, and the justification —
// that it is the cheapest question whose answer is exactly the one needed — is
// worth being able to find.
const probeMethod = "/alchemy.v1.Alchemy/GetJob"
