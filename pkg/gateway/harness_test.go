package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/gateway"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// testToken is the bearer the fixture authenticates with. It is the real
// service's token: the gateway holds none of its own, which is the property
// most of these tests are about.
const testToken = "s3cr3t-token"

// harness is the fake pipeline, the same shape pkg/service's tests use. The
// gateway is exercised against the real service rather than a stub, because
// the thing under test is a translation and a translation tested against a
// mock of the thing it translates tests nothing.
type harness struct {
	run   func(ctx context.Context, jobID string, spec service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error)
	store job.Store
	// maxResultBytes is §8.4's refusal threshold, which one test needs to be
	// able to make small enough to trip.
	maxResultBytes int
}

func (h harness) Run(ctx context.Context, id string, spec service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error) {
	if h.run == nil {
		return alchemy.Result{}, nil
	}
	return h.run(ctx, id, spec, events, in)
}

// fixture is one running product: the gRPC service, and the gateway in front
// of it. Both are torn down with the test.
type fixture struct {
	http *httptest.Server
	grpc alchemyv1.AlchemyClient
}

func serve(t *testing.T, h harness) *fixture {
	t.Helper()

	svc, err := service.New(service.Config{
		Runner: h, Store: h.store, Token: testToken, Spool: t.TempDir(),
		MaxResultBytes: h.maxResultBytes,
	})
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(svc.UnaryInterceptor()), grpc.StreamInterceptor(svc.StreamInterceptor()))
	alchemyv1.RegisterAlchemyServer(gs, svc)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	handler, err := gateway.New(ctx, conn)
	if err != nil {
		cancel()
		t.Fatalf("gateway.New: %v", err)
	}
	hs := httptest.NewServer(handler)

	t.Cleanup(func() {
		hs.Close()
		cancel()
		_ = conn.Close()
		gs.Stop()
		svc.Close()
	})
	return &fixture{http: hs, grpc: alchemyv1.NewAlchemyClient(conn)}
}

// do issues an HTTP request against the gateway. token empty means no
// Authorization header at all, which is the anonymous caller of auth_test.go.
func (f *fixture) do(t *testing.T, method, path, token string, body io.Reader, headers ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.http.URL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// body reads a response and decodes it as JSON, which is what every non-stream
// route answers with.
func body(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("body is not JSON (%q): %v", string(raw), err)
	}
	return out
}

// authed is the gRPC-side credential, for the setup steps that go in by the
// front door rather than through the gateway.
func authed(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+testToken))
}

// uploadOverGRPC and createOverGRPC set a test up through the service itself,
// so that a gateway test that fails is failing about the gateway.
func (f *fixture) uploadOverGRPC(t *testing.T, name string, kind alchemyv1.SourceKind, data []byte) string {
	t.Helper()
	stream, err := f.grpc.UploadSource(authed(context.Background()))
	if err != nil {
		t.Fatalf("UploadSource: %v", err)
	}
	if err := stream.Send(&alchemyv1.SourceChunk{Name: name, Kind: kind, Data: data}); err != nil {
		t.Fatalf("send: %v", err)
	}
	src, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
	return src.GetId()
}

func (f *fixture) createOverGRPC(t *testing.T, req *alchemyv1.CreateJobRequest) string {
	t.Helper()
	j, err := f.grpc.CreateJob(authed(context.Background()), req)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return j.GetId()
}

// awaitState polls GetJob over gRPC until the job arrives where the test needs
// it. Polling belongs in a test fixture and nowhere else.
func (f *fixture) awaitState(t *testing.T, id string, want alchemyv1.JobState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, err := f.grpc.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: id})
		if err == nil && j.GetState() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s never reached %s", id, want)
}

// aDDLJob is the shortest path to a finished job: a deterministic source, no
// ontology question, an empty result.
func (f *fixture) aDDLJob(t *testing.T) string {
	t.Helper()
	src := f.uploadOverGRPC(t, "schema.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("CREATE TABLE t (id int);"))
	return f.createOverGRPC(t, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
}

// disputed is a result with an unanswered conflict in it, which is how §7.3's
// held job — and therefore FAILED_PRECONDITION — is reached.
func disputed() alchemy.Result {
	return alchemy.Result{
		Conflicts: []alchemy.Conflict{{
			Kind:    alchemy.ConflictEntityType,
			Subject: "Acme",
			Detail:  "a CSV says Customer and a contract says Supplier",
			Left:    alchemy.Claim{Statement: "Customer", Provenance: alchemy.Provenance{Source: "crm.csv", Producer: alchemy.ProducerTabular}},
			Right:   alchemy.Claim{Statement: "Supplier", Provenance: alchemy.Provenance{Source: "deal.pdf", Producer: alchemy.ProducerLLMExtract}},
		}},
		Counts: alchemy.Counts{Conflicts: 1},
	}
}

var _ = review.Decision{}
var _ = strings.TrimSpace
