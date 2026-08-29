package service_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// testToken is the bearer the harness authenticates with. Every test that
// expects to be let in uses authed(); the one that does not is auth_test.go.
const testToken = "s3cr3t-token"

// harness is the fake half of every test in this package. §"Your tests must
// not need a real model, a real pipeline, or a network": the runner is a
// function, the store is the in-memory one, and the transport is a bufconn
// pipe, so nothing here touches a socket or an endpoint.
type harness struct {
	// run stands in for the pipeline. Nil means a runner that returns an empty
	// result immediately.
	run func(ctx context.Context, jobID string, spec service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error)
	// store overrides the default in-memory job store, for the tests that need
	// a particular capacity or clock.
	store job.Store
	// maxResultBytes overrides the size at which GetResult refuses (§8.4).
	maxResultBytes int
	pageSize       int
	spool          string
	// sweepEvery drives the expiry sweep; zero leaves the server default.
	sweepEvery time.Duration
}

func (h harness) Run(ctx context.Context, jobID string, spec service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error) {
	if h.run == nil {
		return alchemy.Result{}, nil
	}
	return h.run(ctx, jobID, spec, events, in)
}

// authed attaches the bearer the harness's server expects.
func authed(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+testToken))
}

// dial starts a server over an in-process pipe and returns a client for it.
// Both are closed when the test ends, which is also how the goroutine-leak
// tests get a server that really does shut down.
func dial(t *testing.T, h harness) alchemyv1.AlchemyClient {
	t.Helper()
	_, cli := serve(t, h)
	return cli
}

func serve(t *testing.T, h harness) (*service.Server, alchemyv1.AlchemyClient) {
	t.Helper()
	srv, conn := start(t, h)
	return srv, alchemyv1.NewAlchemyClient(conn)
}

// connect hands back the raw connection, which the auth table test needs: it
// calls every RPC generically off the generated descriptor rather than through
// the typed client, so that a method added later is covered without anybody
// adding a line to it.
func connect(t *testing.T, h harness) *grpc.ClientConn {
	t.Helper()
	_, conn := start(t, h)
	return conn
}

func start(t *testing.T, h harness) (*service.Server, *grpc.ClientConn) {
	t.Helper()
	if h.spool == "" {
		h.spool = t.TempDir()
	}
	srv, err := service.New(service.Config{
		Runner:         h,
		Store:          h.store,
		Token:          testToken,
		Spool:          h.spool,
		MaxResultBytes: h.maxResultBytes,
		PageSize:       h.pageSize,
		SweepEvery:     h.sweepEvery,
	})
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(srv.UnaryInterceptor()), grpc.StreamInterceptor(srv.StreamInterceptor()))
	alchemyv1.RegisterAlchemyServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
		srv.Close()
	})
	return srv, conn
}

// waitFor polls until cond holds or the test's patience runs out. It exists so
// that a test asserting "the runner saw the decision" says how long it waited
// rather than sleeping a magic number and hoping.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// latch is a one-shot signal a fake runner can hold a job open with.
type latch struct {
	once sync.Once
	ch   chan struct{}
}

func newLatch() *latch { return &latch{ch: make(chan struct{})} }

func (l *latch) release() { l.once.Do(func() { close(l.ch) }) }

func (l *latch) wait() <-chan struct{} { return l.ch }

// staticResult is the fake pipeline for tests that care about what comes back
// rather than about how it got there.
func staticResult(res alchemy.Result) func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
	return func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
		return res, nil
	}
}

func upload(t *testing.T, cli alchemyv1.AlchemyClient, name string, kind alchemyv1.SourceKind, data []byte) string {
	t.Helper()
	stream, err := cli.UploadSource(authed(context.Background()))
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

func create(t *testing.T, cli alchemyv1.AlchemyClient, req *alchemyv1.CreateJobRequest) *alchemyv1.Job {
	t.Helper()
	j, err := cli.CreateJob(authed(context.Background()), req)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return j
}

// awaitState polls GetJob. Polling is right here and nowhere else in the
// service: a test watching for a state change is not the operator §6 designed
// WatchJob for, and using the stream would make every test depend on the one
// RPC most likely to be the thing under test.
func awaitState(t *testing.T, cli alchemyv1.AlchemyClient, id string, want alchemyv1.JobState) {
	t.Helper()
	var last alchemyv1.JobState
	waitFor(t, "job "+id+" to reach "+want.String(), func() bool {
		j, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: id})
		if err != nil {
			return false
		}
		last = j.GetState()
		return last == want
	})
	_ = last
}
