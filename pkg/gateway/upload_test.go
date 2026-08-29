package gateway_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/gateway"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// How a curl user uploads a 10GB dump.
//
// The generated mapping for a client-streaming RPC is a body of JSON
// SourceChunk objects, which means base64 and means a client that has to frame
// the file itself. Nobody does that from a shell, and §8.4 says the bytes must
// not be buffered anyway — so the gateway also accepts the file as itself,
// with the metadata in the query string:
//
//	curl -X POST 'http://host/v1/sources?name=dump.sql&kind=SOURCE_KIND_DDL' \
//	     -H 'Content-Type: application/octet-stream' --data-binary @dump.sql
//
// The body here is several times the frame the gateway allocates, because a
// test that fits in one frame proves nothing about the file that does not.
func TestARawBodyUploadsWithoutBeingBuffered(t *testing.T) {
	f := serve(t, harness{})
	payload := bytes.Repeat([]byte("CREATE TABLE t (id int);\n"), 400_000)
	if len(payload) <= gateway.UploadFrameBytes {
		t.Fatalf("the payload is %d bytes, which fits in one %d byte frame; this test would prove nothing", len(payload), gateway.UploadFrameBytes)
	}

	resp := f.do(t, http.MethodPost,
		"/v1/sources?name=dump.sql&kind=SOURCE_KIND_DDL&media_type=application/sql",
		testToken, bytes.NewReader(payload),
		"Content-Type", "application/octet-stream")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body(t, resp))
	}
	got := body(t, resp)
	if got["name"] != "dump.sql" {
		t.Errorf("name = %v, want dump.sql", got["name"])
	}
	if got["kind"] != "SOURCE_KIND_DDL" {
		t.Errorf("kind = %v, want SOURCE_KIND_DDL", got["kind"])
	}
	// size arrives as a string: proto3 int64 is JSON-encoded as one, because a
	// number that large is not safe in JavaScript. That is protojson's rule and
	// the gateway does not second-guess it.
	if got["size"] != "10000000" {
		t.Errorf("size = %v, want the %d bytes that were sent", got["size"], len(payload))
	}

	// The upload is only real if a job can be created from it.
	id := f.createOverGRPC(t, &alchemyv1.CreateJobRequest{
		SourceIds: []string{got["id"].(string)}, Ontology: "crm"})
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)
}

// The generated mapping still works, and must: it is what a generated client
// built from the OpenAPI document will send, and the raw form is an addition
// for humans rather than a replacement.
func TestTheGeneratedJSONUploadMappingStillWorks(t *testing.T) {
	f := serve(t, harness{})
	frames := `{"name":"schema.sql","kind":"SOURCE_KIND_DDL","data":"Q1JFQVRFIFRBQkxFIHQ="}` +
		`{"data":"IChpZCBpbnQpOw=="}`

	resp := f.do(t, http.MethodPost, "/v1/sources", testToken, bytes.NewReader([]byte(frames)),
		"Content-Type", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body(t, resp))
	}
	if got := body(t, resp); got["size"] != "24" {
		t.Errorf("size = %v, want the 24 bytes of the two frames", got["size"])
	}
}

// A source with no kind is the service's refusal to make, not the gateway's:
// the message a caller gets should name the four readers, which is a fact
// about the product rather than about HTTP.
func TestAnUnkindedRawUploadIsRefusedByTheService(t *testing.T) {
	f := serve(t, harness{})
	resp := f.do(t, http.MethodPost, "/v1/sources?name=dump.sql", testToken,
		bytes.NewReader([]byte("x")), "Content-Type", "application/octet-stream")

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if msg, _ := body(t, resp)["message"].(string); msg == "" {
		t.Fatal("no message")
	} else if want := "tabular, ddl, document or graph"; !contains(msg, want) {
		t.Errorf("message = %q, which is not the service's own sentence about %s", msg, want)
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
