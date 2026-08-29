package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestBuildRefusesWithoutAToken(t *testing.T) {
	if _, err := build(settings{spool: t.TempDir()}, ""); !errors.Is(err, service.ErrNoToken) {
		t.Fatalf("build with no token: %v, want service.ErrNoToken", err)
	}
}

// SIGINT and SIGTERM arrive as a cancelled context. What has to follow is
// GracefulStop — stop accepting, let what is running see a cancelled ctx — and
// serve returning rather than the process being killed from under it.
func TestServeStopsGracefullyOnACancelledContext(t *testing.T) {
	sv, err := build(settings{spool: t.TempDir(), capacity: 4, sweepEvery: time.Minute}, "tok")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sv.serve(ctx, lis, nil) }()

	// It is up: an authenticated call is answered.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	call, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	call = metadata.AppendToOutgoingContext(call, "authorization", "Bearer tok")
	if _, err := alchemyv1.NewAlchemyClient(conn).GetJob(call, &alchemyv1.GetJobRequest{JobId: "nope"}); err == nil {
		t.Fatal("GetJob for a job that does not exist should have failed")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return after its context was cancelled")
	}

	// Stopped accepting: the listener is closed.
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("the listener is still accepting after a graceful stop")
	}
}
