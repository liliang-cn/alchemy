package gateway

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

// Server-Sent Events, for the two server-streaming RPCs.
//
// §6 is unkind about SSE and it is right: "over HTTP it is either polling or
// SSE, and SSE is a stream pretending to be a response." That is the argument
// for gRPC being the service and it is not weakened by what is here. What is
// here is the answer to the other half of §6 — "because browsers exist" — and
// the fact is that a browser reading a stream has EventSource and nothing
// else. `fetch` with a ReadableStream can consume the newline-delimited JSON
// this defaults to, but it cannot reconnect, and a progress view that goes
// blank when a laptop lid closes is not a progress view.
//
// So SSE is offered and is never the default: a caller gets it by asking for
// it with `Accept: text/event-stream`, and the events, their order and their
// contents are byte for byte the ones the default framing carries. The only
// difference is the frame around them. That is what keeps this a translation:
// there is no SSE-only field, no heartbeat the gRPC stream does not send, and
// no event this invents when the service is quiet.

const sseContentType = "text/event-stream"

// sseMarshaler frames what the JSON marshaler produced.
//
// It embeds runtime.Marshaler rather than reimplementing one because the
// payload must be identical to the default framing's — the same protojson
// settings, the same field names. Only Marshal and the delimiter differ, which
// is exactly the difference between the two framings and nothing more.
type sseMarshaler struct {
	runtime.Marshaler
}

func (m sseMarshaler) ContentType(any) string { return sseContentType }

// StreamContentType is what grpc-gateway asks for on a streaming response.
// Both are answered so that a caller who asked for SSE gets it on a unary
// route too rather than a content type that contradicts the framing.
func (m sseMarshaler) StreamContentType(any) string { return sseContentType }

func (m sseMarshaler) Marshal(v any) ([]byte, error) {
	payload, err := m.Marshaler.Marshal(v)
	if err != nil {
		return nil, err
	}
	// A newline inside the payload would end the SSE data field early and
	// silently truncate the event. protojson does not emit them today; relying
	// on that would make this correct by luck.
	var b bytes.Buffer
	b.WriteString("data: ")
	b.WriteString(strings.ReplaceAll(strings.TrimRight(string(payload), "\n"), "\n", "\ndata: "))
	return b.Bytes(), nil
}

// Delimiter ends an event. Two newlines, which is what an SSE frame is.
func (m sseMarshaler) Delimiter() []byte { return []byte("\n\n") }

// sseHeaders sets the two headers that keep an SSE stream a stream.
//
// They are set from the request rather than from the marshaler because
// grpc-gateway gives a marshaler no way to add headers, and because both are
// instructions to the infrastructure between the gateway and the browser
// rather than statements about the payload: no-cache so an intermediary does
// not serve a stale prefix, and X-Accel-Buffering so nginx — which buffers
// proxied responses by default — does not hold the events until the job ends
// and hand the operator the whole two-hour import at once.
func sseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, accept := range r.Header.Values("Accept") {
			if accept == sseContentType {
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Accel-Buffering", "no")
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}
