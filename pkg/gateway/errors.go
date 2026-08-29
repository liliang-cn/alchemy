package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// httpError is where a gRPC code becomes an HTTP status.
//
// Almost all of it is grpc-gateway's default mapping, and that is on purpose:
// the defaults already carry pkg/service's four load-bearing codes to the four
// statuses a client acts on — NotFound to 404, InvalidArgument to 400,
// ResourceExhausted to 429 and Unauthenticated to 401 — and changing a mapping
// that is already right would be the gateway having an opinion about what the
// service meant.
//
// One code is remapped, and the reason is in errors.go of pkg/service:
//
//	"FailedPrecondition and InvalidArgument are separated on purpose. A caller
//	asking for the result of a job that is still running has sent a request
//	that will be correct in a minute; a caller asking for the result of
//	nothing has sent one that never will be."
//
// grpc-gateway's default sends both to 400. That collapses the distinction the
// service went out of its way to draw, at the very last step, and hands a
// client one code meaning two opposite instructions: retry forever, or give up
// immediately, each wrong half the time. 412 Precondition Failed says the
// request was fine and the state was not, which is exactly what a job held for
// a person (§7.3) or a result too large for one message (§8.4) is. A retry
// loop can act on 412 and 429 and give up on 400, which is the behaviour the
// gRPC contract already described.
//
// The remap is done by wrapping in runtime.HTTPStatusError rather than by
// writing a response here, so that the body, the headers and the trailer
// handling all stay the gateway's — a hand-rolled error writer is how the one
// route that skipped WWW-Authenticate gets shipped.
func httpError(ctx context.Context, mux *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	var already *runtime.HTTPStatusError
	if !errors.As(err, &already) && status.Code(err) == codes.FailedPrecondition {
		err = &runtime.HTTPStatusError{HTTPStatus: http.StatusPreconditionFailed, Err: err}
	}
	runtime.DefaultHTTPErrorHandler(ctx, mux, m, w, r, err)
}
