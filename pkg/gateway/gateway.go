// Package gateway is the last sentence of DESIGN.md §6: "A REST/JSON gateway
// is generated from the same definitions, because a buyer evaluating the
// product should be able to curl it, and because browsers exist. The gateway
// is a translation, never a second source of truth about what the service
// does."
//
// The second half of that sentence is the whole design of this package, and it
// is a constraint rather than an aspiration. There is no validation here, no
// defaulting, no retry, no cache, and no route that does not correspond to an
// RPC. Every refusal a caller sees was decided by pkg/service and is being
// re-spelled in HTTP; the only opinions this package holds are about HTTP
// itself — which status code carries which gRPC code, how a stream is framed,
// and how a credential travels in a header instead of in metadata.
//
// What is hand-written here is only what protoc could not generate: three
// pieces of content negotiation (raw uploads, Server-Sent Events, and the
// status mapping) and one refusal (Review, which has no honest translation).
// Each has its own file and its own reason.
//
// There is one deliberate exception to "no route that does not correspond to
// an RPC", and it is declared rather than assumed: the browser view under /ui
// (view.go). A page backs no RPC, so it cannot be a translation of one; what
// keeps it inside the rule's intent is that every one of its handlers ends in
// an RPC call carrying the caller's own credential, so it can ask the service
// nothing a buyer with curl could not ask. The exception is enumerated in
// Views(), each entry carrying the sentence that justifies it, for the same
// reason Refusals() is enumerated: an exception a test reads is one nobody can
// take by accident.
package gateway

// The generated half of this package's world — the .pb.gw.go handlers and the
// OpenAPI document the tests read their route table out of — is rebuilt by the
// Makefile at the repository root. The line below is so that `go generate
// ./...` finds it too: a generation step that only one person knows the
// command for is a generation step that stops being run.
//go:generate make -C ../.. generate

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

// New builds the HTTP surface in front of an already-dialled gRPC connection.
//
// It takes a connection rather than an address because the gateway has no
// business deciding how the service is reached — transport credentials, a Unix
// socket, an in-process pipe in a test — and because a package that dials is a
// package that has to own retry and shutdown policy for something it does not
// operate.
//
// ctx bounds the registration: grpc-gateway watches it to release the handlers
// when the process is going away.
func New(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, jsonMarshaler()),
		// See upload.go: a corpus arrives as itself rather than as base64
		// inside JSON, because §8.4's 10GB dump has to be curl-able.
		runtime.WithMarshalerOption(rawUploadContentType, chunkMarshaler{Marshaler: jsonMarshaler()}),
		// See sse.go: the same events, framed for EventSource, and only when
		// a caller asks for them by Accept.
		runtime.WithMarshalerOption(sseContentType, sseMarshaler{Marshaler: jsonMarshaler()}),
		// See errors.go: one gRPC code is carried to a different status than
		// the gateway's default, because the default loses a distinction
		// pkg/service was careful to make.
		runtime.WithErrorHandler(httpError),
	)
	if err := alchemyv1.RegisterAlchemyHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	// The refusals are registered after the generated routes and never overlap
	// them: an RPC has a translation or it has a refusal, never both, which is
	// what routes_test.go's coverage check enforces.
	client := alchemyv1.NewAlchemyClient(conn)
	for _, ref := range Refusals() {
		if err := mux.HandlePath(ref.Method, ref.Path, refuse(mux, client, ref)); err != nil {
			return nil, err
		}
	}
	// The browser view is mounted in front of the translated routes rather than
	// on them. It is the one part of this package that backs no RPC, which is
	// why it is declared in Views() and reserved a prefix of its own: see
	// view.go for the argument, and routes_test.go for the test that will not
	// let the exception be taken quietly.
	//
	// rawUploads wraps rather than routes: it decides how one body is spelled
	// and never where a request goes.
	return rawUploads(sseHeaders(viewRoutes(mux, newViewer(client)))), nil
}

// jsonMarshaler is the JSON a buyer reads, and its two settings are both
// answers to "what did the document promise them".
//
// UseProtoNames because DESIGN.md writes its own JSON in snake_case — §5b's
// counts block is `"chunks_empty": 23` — and a gateway emitting `chunksEmpty`
// would be describing a different product than the document that sold it. The
// generated OpenAPI is produced with json_names_for_fields=false for the same
// reason, so the document and the wire agree.
//
// EmitDefaultValues because a zero in the counts block is a fact, not an
// absence. "violations": 0 is the sentence §5 says every graph must carry;
// omitting the key leaves a reader unable to tell "none" from "not measured",
// which is precisely the distinction the whole design is built to preserve.
// Empty messages and empty lists are still omitted — an absent provenance is
// genuinely absent, and emitting one would invent a fact.
func jsonMarshaler() *runtime.JSONPb {
	return &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			UseProtoNames:     true,
			EmitDefaultValues: true,
		},
		// DiscardUnknown stays false. A CreateJob body with a misspelled field
		// is refused rather than silently ignored, which is §2.1's lesson
		// applied to a request: a guess that does not announce itself is a bug
		// with a three-month fuse, and "the ontology I sent was ignored" is
		// exactly that bug.
		UnmarshalOptions: protojson.UnmarshalOptions{},
	}
}
