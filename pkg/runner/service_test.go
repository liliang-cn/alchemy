package runner_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/runner"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const testToken = "test-token"

// The rule this whole adapter exists to keep, tested where it is actually
// observable: §7.3 says a job that finds a conflict does not finish, and the
// service is the one place that decides it. If the runner returned the
// pipeline's *HeldError, the job below would be FAILED instead of NEEDS_REVIEW
// and the conflict would be unreachable — a question turned into a defect.
func TestHeldPipelineRunReachesTheServiceAsAHeldJob(t *testing.T) {
	client, ctx := serve(t)

	src := upload(t, ctx, client, "schema.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL,
		"CREATE TABLE users (id INT);\nCREATE TABLE users (id INT, email TEXT);")
	job, err := client.CreateJob(ctx, &alchemyv1.CreateJobRequest{SourceIds: []string{src.GetId()}})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	final := settle(t, ctx, client, job.GetId())
	if final.GetState() != alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW {
		t.Fatalf("job state = %v (error %q), want NEEDS_REVIEW", final.GetState(), final.GetError())
	}

	// And the question is reachable. This is the half a failed job would lose:
	// the service can only queue what it was handed a result to queue from, so
	// a runner that returned the error would leave a person nothing to answer.
	stream, err := client.Review(ctx)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if err := stream.Send(&alchemyv1.ReviewDecision{JobId: job.GetId()}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	item, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if item.GetKind() != alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT {
		t.Fatalf("queued item kind = %v, want a conflict", item.GetKind())
	}
}

// A clean job still succeeds through the same path, so the test above is about
// the hold and not about everything being held.
func TestCleanRunReachesTheServiceAsASucceededJob(t *testing.T) {
	client, ctx := serve(t)

	src := upload(t, ctx, client, "schema.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL,
		"CREATE TABLE users (id INT PRIMARY KEY);")
	job, err := client.CreateJob(ctx, &alchemyv1.CreateJobRequest{SourceIds: []string{src.GetId()}})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	final := settle(t, ctx, client, job.GetId())
	if final.GetState() != alchemyv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("job state = %v (error %q), want SUCCEEDED", final.GetState(), final.GetError())
	}
}

// serve starts the real server over a real listener with the real runner
// behind it, and returns an authenticated client.
func serve(t *testing.T) (alchemyv1.AlchemyClient, context.Context) {
	t.Helper()
	run, err := runner.New(runner.Config{Factory: stubFactory{}})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	srv, err := service.New(service.Config{Runner: run, Token: testToken, Spool: t.TempDir()})
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	alchemyv1.RegisterAlchemyServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(func() { gs.Stop(); srv.Close() })

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+testToken)
	return alchemyv1.NewAlchemyClient(conn), ctx
}

func upload(t *testing.T, ctx context.Context, c alchemyv1.AlchemyClient, name string, kind alchemyv1.SourceKind, body string) *alchemyv1.Source {
	t.Helper()
	stream, err := c.UploadSource(ctx)
	if err != nil {
		t.Fatalf("UploadSource: %v", err)
	}
	if err := stream.Send(&alchemyv1.SourceChunk{Name: name, Kind: kind, Data: []byte(body)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	src, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
	return src
}

// settle polls until the job stops moving. Polling rather than WatchJob keeps
// this test about the state the service recorded, which is the thing at issue.
func settle(t *testing.T, ctx context.Context, c alchemyv1.AlchemyClient, id string) *alchemyv1.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j, err := c.GetJob(ctx, &alchemyv1.GetJobRequest{JobId: id})
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		switch j.GetState() {
		case alchemyv1.JobState_JOB_STATE_PENDING, alchemyv1.JobState_JOB_STATE_RUNNING:
		default:
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the job never left PENDING/RUNNING")
	return nil
}

// stubFactory is a caller that supplied no endpoints, which is all a DDL job
// needs: §2.1's first lesson is that a schema states its own meaning.
type stubFactory struct{}

func (stubFactory) LLM(runner.Endpoint) (alchemy.LLM, error)           { return nil, errNoProvider }
func (stubFactory) Embedder(runner.Endpoint) (alchemy.Embedder, error) { return nil, errNoProvider }
func (stubFactory) OCR(runner.Endpoint) (alchemy.OCR, error)           { return nil, errNoProvider }

var errNoProvider = errors.New("no provider is configured in this test")
