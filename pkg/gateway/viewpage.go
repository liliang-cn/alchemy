package gateway

import (
	_ "embed"
	"net/http"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The two documents the view serves.
//
// They are files rather than Go string literals so that neither is edited
// through a compiler's eyes — a stylesheet inside a backtick string is a
// stylesheet nobody reformats — and so that no Go file in this package carries
// a page's worth of markup.
//
// Neither loads anything from anywhere. There is no script tag with a src, no
// stylesheet link, no font, no image: the whole viewer, including the force
// layout, the perspective projection and the renderer, is in the document. The
// reason is the deployment, not purity. This runs on a private VM, and a page
// that fetches a graph library from a CDN is a page that renders a blank
// rectangle on a machine with no route to the internet — silently, because a
// script that fails to load fires no error a person sees. Vendoring the
// library instead would have traded that for a megabyte of somebody else's
// minified code in this repository; writing the renderer costs a few hundred
// lines of JavaScript that can be read.
//
//go:embed view.html
var viewHTML []byte

//go:embed signin.html
var signInHTML []byte

func signInPage() []byte { return signInHTML }

// landing is the page with no job named yet.
//
// It serves the same document as page(), which then asks which job to open.
// There is no list of jobs to offer, and that is §4 rather than an omission:
// the service holds work in progress, not a catalogue, and inventing a
// ListJobs here would be the gateway becoming a second source of truth about
// what the service knows.
func (v *viewer) landing(w http.ResponseWriter, r *http.Request) {
	if _, ok := v.authorize(r); !ok {
		v.refuse(w, r)
		return
	}
	writePage(w, viewHTML)
}

// page serves the viewer for one job.
//
// It probes GetJob first, for the reason review.go's refusal probes it: the
// answer a caller gets about a job that is not theirs, or not there, must be
// the service's answer and not this package's. A page that rendered its shell
// for any job ID and left the 404 to the first fetch would be announcing which
// of a buyer's job IDs are real to anyone who can reach the port.
func (v *viewer) page(w http.ResponseWriter, r *http.Request) {
	ctx, ok := v.authorize(r)
	if !ok {
		v.refuse(w, r)
		return
	}
	if _, err := v.client.GetJob(ctx, &alchemyv1.GetJobRequest{JobId: r.PathValue("job_id")}); err != nil {
		http.Error(w, status.Convert(err).Message(), viewStatus(err))
		return
	}
	writePage(w, viewHTML)
}

// writePage sends a document with the headers that make it behave.
//
// no-store because the page is a view of work in progress and a cached copy of
// a held job that has since been resolved is a lie with a timestamp. The
// referrer policy because the URL contains a job ID, and nosniff because a
// document served as HTML should be read as one whatever a browser guesses.
func writePage(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

// viewStatus carries a gRPC code to an HTTP status for the routes grpc-gateway
// does not own.
//
// It is deliberately the same mapping errors.go makes, including the one place
// that mapping departs from grpc-gateway's default: FailedPrecondition becomes
// 412 rather than 400, because "the request was fine and the state was not" is
// exactly what a job held for a person is, and a view that reported a held job
// as a malformed request would be telling the person who has to resolve the
// conflict that they typed something wrong.
func viewStatus(err error) int {
	switch status.Code(err) {
	case codes.OK:
		return http.StatusOK
	case codes.NotFound:
		return http.StatusNotFound
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.FailedPrecondition:
		return http.StatusPreconditionFailed
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Canceled:
		// 499 is nginx's, not the RFC's, and it is the honest answer: nobody
		// is left to read a status, and a 500 here would put a client's
		// disconnection in the log as this service's fault.
		return 499
	default:
		return http.StatusInternalServerError
	}
}
