package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// testSettings is a settings value that runs, with the spool under the test's
// own directory so nothing is left behind.
func testSettings(t *testing.T) settings {
	t.Helper()
	return settings{
		addr:             "127.0.0.1:0",
		spool:            t.TempDir(),
		store:            "memory",
		capacity:         4,
		sweepEvery:       time.Minute,
		modelConcurrency: 2,
	}
}

// authed puts the credential on a call the way a client does.
func authed(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer the-secret")
}

// serveForTest starts the whole program the way main does and returns a client
// to it. It goes through build so that a wiring mistake — a component the
// service exposes and the binary forgets to install — fails here rather than
// in production, which is the failure this file exists for.
func serveForTest(t *testing.T, s settings) alchemyv1.AlchemyClient {
	t.Helper()
	sv, err := build(s, "the-secret")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = sv.serve(ctx, lis, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})
	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return alchemyv1.NewAlchemyClient(cc)
}

// The service exposes interceptors; a binary that does not install them serves
// every RPC to anyone. pkg/service's own auth tests install them in their
// harness, so they prove the interceptor works and say nothing about whether
// the program uses it. This is that second question, and it is the one an
// operator is actually exposed to.
func TestTheBuiltServerRefusesAnUnauthenticatedCall(t *testing.T) {
	c := serveForTest(t, testSettings(t))
	_, err := c.GetJob(context.Background(), &alchemyv1.GetJobRequest{JobId: "whatever"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("GetJob with no credentials: code = %v (err %v), want Unauthenticated", status.Code(err), err)
	}
}

// The stream interceptor is installed separately from the unary one, so
// forgetting one of the two is a thing that happens.
func TestTheBuiltServerRefusesAnUnauthenticatedStream(t *testing.T) {
	c := serveForTest(t, testSettings(t))
	st, err := c.UploadSource(context.Background())
	if err == nil {
		err = st.Send(&alchemyv1.SourceChunk{Name: "x.sql"})
		if err == nil {
			_, err = st.CloseAndRecv()
		}
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("UploadSource with no credentials: code = %v (err %v), want Unauthenticated", status.Code(err), err)
	}
}

// A spool directory that does not exist yet is the ordinary case on a fresh
// machine: an operator names a path under /var/lib and expects the service to
// own it. Failing at the first upload instead — long after a clean start —
// makes a configuration mistake look like a bug in uploading.
func TestASpoolDirectoryIsCreatedRatherThanRequired(t *testing.T) {
	s := testSettings(t)
	s.spool = filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	c := serveForTest(t, s)
	ctx := authed(context.Background())

	st, err := c.UploadSource(ctx)
	if err != nil {
		t.Fatalf("UploadSource: %v", err)
	}
	if err := st.Send(&alchemyv1.SourceChunk{
		Name: "schema.sql",
		Kind: alchemyv1.SourceKind_SOURCE_KIND_DDL,
		Data: []byte("CREATE TABLE t (id INT PRIMARY KEY);"),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	src, err := st.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
	if src.GetId() == "" {
		t.Fatal("the upload returned no source id")
	}
	if _, err := os.Stat(s.spool); err != nil {
		t.Errorf("the spool directory was not created: %v", err)
	}
}
