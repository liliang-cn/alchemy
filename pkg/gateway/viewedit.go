package gateway

import (
	"io"
	"net/http"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// The three routes a browser needs in order to change anything, and the reason
// they exist here rather than the page simply calling /v1.
//
// The page cannot call /v1. The session cookie is scoped to ViewPrefix
// (viewsession.go says why: "nothing under /v1 reads a cookie, and a
// credential that travels where nothing reads it is a credential exposed for
// no reason") and it is HttpOnly, so the page's own script can neither send it
// to /v1 nor read it in order to build an Authorization header — and even if
// it could, the cookie is a ticket and not the token, so there would be
// nothing to put in the header. The alternatives were both worse than a
// proxy: widening the cookie's Path to "/" puts the browser's credential on
// every RPC call in the product, and handing the token back to the page turns
// the receipt back into the credential and loses the whole argument HttpOnly
// was making.
//
// So these three are proxies, and they are the narrowest kind: each unmarshals
// the body into the generated request message with the same marshaller every
// other route uses, calls the one RPC, and marshals the reply back. There is
// no validation here, no defaulting and no second opinion — a decision with no
// reviewer on it is refused by pkg/service in pkg/service's words, and this
// file's only contribution is the status code that carries the refusal.
//
// What they carry is DESIGN.md §4 showing through the UI. A finished result is
// not mutable state on a server, so "modify" and "delete" are not one
// operation: on a job still held for review they are decisions on its queue,
// and on a job already delivered the queue is closed and the honest act is an
// assertion that supersedes — which states that a record is over and removes
// nothing, because there is nothing here to remove.

// viewMaxBody bounds a request body the page sends.
//
// A decision batch is a queue somebody worked through and an assertion is a
// few records somebody typed; neither is a corpus, and the route that takes a
// corpus is upload.go, which has its own framing for exactly that reason. The
// bound is here rather than left to the service because an unbounded read is
// this process's memory, and the refusal a caller gets for exceeding it should
// name a size rather than arrive as a connection that died.
const viewMaxBody = 4 << 20

// findings hands the browser the queue of a job that has stopped.
//
// It is a read and it is on this list because a decision names an item, and an
// item id exists nowhere but here. The graph JSON carries conflicts and
// violations, which are findings and not questions; the queue is the questions,
// with the ids the service will accept. A page that derived an id from a
// conflict instead would be inventing a name for something pkg/service already
// named, which is the shape of every bug where a client and a server disagree
// about what a record is called.
func (v *viewer) findings(w http.ResponseWriter, r *http.Request) {
	ctx, ok := v.authorize(r)
	if !ok {
		v.refuse(w, r)
		return
	}
	out, err := v.client.ListFindings(ctx, &alchemyv1.ListFindingsRequest{JobId: r.PathValue("job_id")})
	if err != nil {
		http.Error(w, status.Convert(err).Message(), viewStatus(err))
		return
	}
	writeProto(w, out)
}

// decisions submits a worked-through queue.
//
// The job in the path wins over any job named in the body, which is what the
// generated route for Decide does and is copied deliberately rather than
// improved on: a URL that reads as one job and acts on another is a difference
// between two spellings of one route that nobody finds until it has already
// happened. Everything else — the verb, the signature, whether the item is
// still in the queue — is pkg/service's to judge, and its answers arrive here
// unedited, including the per-item rejections that say a decision was carried
// and applied to nothing.
func (v *viewer) decisions(w http.ResponseWriter, r *http.Request) {
	ctx, ok := v.authorize(r)
	if !ok {
		v.refuse(w, r)
		return
	}
	req := &alchemyv1.DecideRequest{}
	if !readProto(w, r, req) {
		return
	}
	req.JobId = r.PathValue("job_id")
	out, err := v.client.Decide(ctx, req)
	if err != nil {
		http.Error(w, status.Convert(err).Message(), viewStatus(err))
		return
	}
	writeProto(w, out)
}

// assertions records a fact a person states, and is how the browser adds
// anything at all.
//
// It is also how a delivered job's records are corrected and retired, because
// §4 leaves no other honest shape: the graph the person is looking at is the
// output of a job that has finished, so there is nothing on this server to
// edit. An assertion that supersedes says the old record is over and names who
// said so, which is a claim that survives into the next run's provenance
// rather than a mutation of something nobody holds.
//
// The page sends no ontology, and that is a limitation rather than a decision
// about vocabularies: the job's ontology document is not on the graph JSON, so
// the view has none to send. It says so on screen, because an assertion
// checked against nothing comes back clean and a person who read that as "the
// types are fine" has been told the opposite of the truth.
func (v *viewer) assertions(w http.ResponseWriter, r *http.Request) {
	ctx, ok := v.authorize(r)
	if !ok {
		v.refuse(w, r)
		return
	}
	req := &alchemyv1.AssertRequest{}
	if !readProto(w, r, req) {
		return
	}
	out, err := v.client.Assert(ctx, req)
	if err != nil {
		http.Error(w, status.Convert(err).Message(), viewStatus(err))
		return
	}
	writeProto(w, out)
}

// readProto decodes a request body into the message the RPC takes, or answers
// the caller and reports that it did.
//
// The unmarshaller is jsonMarshaler()'s, so a field the page spells the way
// the OpenAPI document spells it is accepted here exactly as it is on /v1 —
// and a misspelled one is refused rather than dropped, which is the setting
// gateway.go argues for at length: "the ontology I sent was ignored" is a bug
// with a three-month fuse.
func readProto(w http.ResponseWriter, r *http.Request, into proto.Message) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, viewMaxBody))
	if err != nil {
		http.Error(w, "the request body could not be read, or is larger than this route accepts", http.StatusBadRequest)
		return false
	}
	if len(raw) == 0 {
		// An empty body is a caller who sent nothing rather than a caller who
		// sent an empty object, and the two deserve different sentences: the
		// service's refusal for an empty request would say "you asserted
		// nothing", which is true and unhelpful to somebody whose form never
		// serialised.
		http.Error(w, "the request has no body; this route takes the JSON the matching /v1 route takes", http.StatusBadRequest)
		return false
	}
	if err := jsonMarshaler().Unmarshal(raw, into); err != nil {
		http.Error(w, "the request body is not the JSON this route takes: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// writeProto sends a reply the way every other route in this package sends
// one, so that what the page reads and what a buyer curls are the same
// document with the same field names.
func writeProto(w http.ResponseWriter, msg proto.Message) {
	w.Header().Set("Content-Type", "application/json")
	// A view of work in progress, exactly as the graph is: a cached copy of a
	// queue that has since been decided is a lie with a timestamp.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(marshalProto(msg))
}
