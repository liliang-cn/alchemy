package gateway

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// Uploading a corpus over HTTP.
//
// The generated mapping for a client-streaming RPC is `body: "*"`, which means
// the request body is a sequence of JSON SourceChunk objects with the bytes
// base64-encoded. That is a correct translation and it is unusable by the
// person §6 built the gateway for: nobody frames a 10GB dump into JSON objects
// from a shell, and base64 costs a third of the wire for nothing.
//
// So the gateway also accepts the file as itself. `Content-Type:
// application/octet-stream`, the bytes in the body, and the metadata that the
// gRPC first frame carries in the query string instead:
//
//	POST /v1/sources?name=dump.sql&kind=SOURCE_KIND_DDL&media_type=application/sql
//
// This is content negotiation, not a second endpoint: the same route, the same
// RPC, the same messages on the wire to the service. What changes is only how
// the caller spelled them, which is the kind of thing a gateway is for.
//
// The query string rather than headers because a client-streaming method
// cannot bind path or query parameters through the generated code — grpc-gateway
// refuses them — so something has to read them, and the query string is where
// the OpenAPI document can at least describe them to a human. The metadata
// could equally have ridden in headers; the query string wins because it
// survives being pasted into a browser's address bar, and §6's reason for this
// whole package is that browsers exist.

// UploadFrameBytes is how much of a raw body the gateway forwards in one
// SourceChunk.
//
// It is exported because it is the number the tests must exceed to prove
// anything: §8.4's promise is that a 10GB dump is never held in memory, and a
// test with a body smaller than this frame would pass against an
// implementation that read the whole file first.
//
// A megabyte is chosen against gRPC's 4MB default message limit — comfortably
// under it, so a frame never becomes the truncation §8.4 says a caller should
// never have to discover — and against syscall overhead, which stops mattering
// well below this.
const UploadFrameBytes = 1 << 20

// UploadPath is the route the raw form applies to. It is a constant rather
// than a literal so that routes_test.go can check it against the generated
// OpenAPI document: a path that drifted from the annotation would silently
// turn every raw upload back into a JSON parse failure.
const UploadPath = "/v1/sources"

// rawUploadContentType is the only content type the raw form claims. Anything
// else — including no content type at all — goes to the generated JSON
// mapping, so a client built from the OpenAPI document is unaffected by any of
// this.
const rawUploadContentType = "application/octet-stream"

// sourceBody is a request body that also remembers what the query string said
// about it.
//
// The metadata has to reach the decoder, and the decoder is handed nothing but
// an io.Reader by the generated handler. Rather than smuggle it through a
// package-level map keyed by request — which is a data race waiting for its
// second concurrent upload — the body itself carries it, and the decoder type
// asserts. The seam is small and it is in one file.
type sourceBody struct {
	io.ReadCloser
	name      string
	kind      alchemyv1.SourceKind
	mediaType string
}

// rawUploads wraps the mux, replacing the body of a raw upload with one that
// knows what it is.
//
// It is a wrapper rather than a route of its own because the routing is still
// the generated mux's: this decides how a body is spelled, never where a
// request goes. A request it does not recognise is passed through untouched.
func rawUploads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != UploadPath || !isRaw(r) {
			next.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		kind, err := sourceKind(q.Get("kind"))
		if err != nil {
			// This one refusal really is the gateway's: it is about a query
			// string, which is a thing only HTTP has. Everything else a caller
			// can get wrong about an upload — an unnamed source, an unknown
			// kind, a body over the size limit — is refused by the service, in
			// the service's own words.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = &sourceBody{
			ReadCloser: r.Body,
			name:       q.Get("name"),
			kind:       kind,
			mediaType:  q.Get("media_type"),
		}
		next.ServeHTTP(w, r)
	})
}

func isRaw(r *http.Request) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && kind == rawUploadContentType
}

// sourceKind reads the kind query parameter the way grpc-gateway reads an enum
// anywhere else, so that a caller who learned the spelling from one route does
// not have to learn a second one here.
//
// An absent kind is deliberately not an error. It becomes SOURCE_KIND_UNSPECIFIED
// and the service refuses it with the sentence that names the four readers,
// which is a better answer than anything this function knows how to write.
func sourceKind(raw string) (alchemyv1.SourceKind, error) {
	if raw == "" {
		return alchemyv1.SourceKind_SOURCE_KIND_UNSPECIFIED, nil
	}
	v, err := runtime.Enum(raw, alchemyv1.SourceKind_value)
	if err != nil {
		return 0, fmt.Errorf("kind=%q is not a source kind: %w", raw, err)
	}
	return alchemyv1.SourceKind(v), nil
}

// chunkMarshaler is the inbound half of the raw form. Only NewDecoder is its
// own; everything else is the JSON marshaler, because the *response* to an
// upload is a Source and a caller who sent bytes still wants JSON back.
type chunkMarshaler struct {
	runtime.Marshaler
}

func (m chunkMarshaler) NewDecoder(r io.Reader) runtime.Decoder {
	body, ok := r.(*sourceBody)
	if !ok {
		// The content type said raw and the wrapper did not run, which can
		// only happen if this marshaler is registered for a route rawUploads
		// does not know about. Refusing here rather than guessing keeps the
		// two halves from drifting apart silently.
		return runtime.DecoderFunc(func(any) error {
			return errors.New("a raw body reached the upload decoder without its metadata; " +
				"application/octet-stream is only accepted on " + UploadPath)
		})
	}
	return newChunkDecoder(body)
}

// chunkDecoder turns a stream of bytes into a stream of SourceChunks, a frame
// at a time and never more.
type chunkDecoder struct {
	src *sourceBody
	// named records that the first frame has gone. The service reads the
	// metadata from the first frame only and ignores it afterwards, so
	// repeating it would be harmless — and omitting it is what makes the
	// gateway's frames indistinguishable from a gRPC client's.
	named bool
	// drained is set once the reader has reported its end, so the frame that
	// carried the last bytes is delivered before EOF rather than instead of it.
	drained bool
}

func newChunkDecoder(src *sourceBody) *chunkDecoder { return &chunkDecoder{src: src} }

func (d *chunkDecoder) Decode(v any) error {
	chunk, ok := v.(*alchemyv1.SourceChunk)
	if !ok {
		return fmt.Errorf("gateway: a raw upload decodes into SourceChunk, not %T", v)
	}
	if d.drained {
		return io.EOF
	}

	// A fresh buffer per frame rather than one reused across the upload: gRPC
	// may hold a message past Send — buffering it for a retry, for one — and a
	// buffer refilled underneath it is a corpus that arrives subtly wrong. A
	// megabyte allocation per megabyte forwarded is invisible next to the disk
	// write and the network on the other side.
	buf := make([]byte, UploadFrameBytes)
	n, err := io.ReadFull(d.src, buf)
	switch {
	case err == nil:
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		d.drained = true
	default:
		return err
	}
	if n == 0 && d.named {
		return io.EOF
	}

	chunk.Data = buf[:n]
	if !d.named {
		// An empty body still gets this frame. A source with a name and no
		// bytes is something the service has an opinion about; a stream that
		// ended before naming anything is something only the gateway would
		// have to explain.
		chunk.Name, chunk.Kind, chunk.MediaType = d.src.name, d.src.kind, d.src.mediaType
		d.named = true
	}
	return nil
}
