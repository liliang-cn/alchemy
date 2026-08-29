package main

import (
	"context"
	"fmt"
	"net"

	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/cache"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/runner"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
)

// server is the two halves of the running process: the transport, and the
// service behind it. They are kept together because stopping is two calls in
// one order — stop accepting, then stop the work — and a caller holding only
// one of them cannot get that order right.
type server struct {
	grpc *grpc.Server
	svc  *service.Server
}

// build assembles the program. It is separate from main and from listening so
// that a test can start the whole thing on a port the operating system picks
// and stop it again.
func build(s settings, token string) (*server, error) {
	b, err := modelBudget(s)
	if err != nil {
		return nil, err
	}
	run, err := runner.New(runner.Config{Factory: modelFactory{}, Budget: b, Cache: extractCache(s)})
	if err != nil {
		return nil, err
	}
	svc, err := service.New(service.Config{
		Runner: run,
		// §8.3: in-memory is the single-node default and what a buyer
		// evaluating the product runs. §8.4's admission control is the
		// capacity: a queue that accepts everything is a queue that OOMs.
		Store:      job.New(job.Config{Capacity: s.capacity}),
		Token:      token,
		Spool:      s.spool,
		SweepEvery: s.sweepEvery,
	})
	if err != nil {
		return nil, err
	}
	// The service exposes the credential check; installing it is the binary's
	// job, and forgetting to is not a compile error — the program starts, every
	// RPC works, and it serves the caller's graph to anyone who asks. Both
	// interceptors are needed: unary and streaming are separate chains, and an
	// UploadSource that skipped the check would be the one that mattered.
	gs := grpc.NewServer(
		grpc.UnaryInterceptor(svc.UnaryInterceptor()),
		grpc.StreamInterceptor(svc.StreamInterceptor()),
	)
	alchemyv1.RegisterAlchemyServer(gs, svc)
	return &server{grpc: gs, svc: svc}, nil
}

// modelBudget builds §8.2's budget: how many calls may be in flight against
// one model endpoint.
//
// Zero is off, and that is a real configuration rather than a mistake — one
// node against an endpoint whose limit nobody has declared. pkg/budget refuses
// a non-positive limit for its own good reason (a budget with no bound is not
// a budget), so "off" is expressed by having no budget at all rather than by a
// budget of zero.
func modelBudget(s settings) (budget.Budget, error) {
	if s.modelConcurrency <= 0 {
		return nil, nil
	}
	b, err := budget.NewLocal(budget.Config{Limit: s.modelConcurrency})
	if err != nil {
		return nil, fmt.Errorf("alchemy: -model-concurrency: %w", err)
	}
	return b, nil
}

// extractCache builds §8.2's content-addressed store for extraction results.
//
// Zero or negative is no cache at all rather than a cache of size zero.
// cache.NewMemory(0) is a legitimate value — it returns a working Cache that
// stores nothing — and using it here would be the subtle wrong answer: the
// extractor would consult a store for every chunk, miss, and store nothing,
// paying for the lookup on every chunk of every job forever, and paying it
// over a network the day the store is the shared one §8.3 describes. Off is
// expressed by having nothing, the same way modelBudget expresses it.
func extractCache(s settings) cache.Cache {
	if s.extractCache <= 0 {
		return nil
	}
	return cache.NewMemory(s.extractCache)
}

// serve runs until ctx is cancelled, then stops in the order that makes a
// shutdown graceful rather than merely fast.
//
// GracefulStop first: it stops accepting immediately and lets the calls that
// are already in flight finish, so a client mid-upload gets an answer instead
// of a reset connection. service.Close second: it cancels the context every
// running job hangs off, which is how a job learns the process is going away,
// and waits for them. Doing it the other way round would cancel a job whose
// caller is still being served, and the caller would be told their import
// failed by a service that was about to exit anyway.
func (sv *server) serve(ctx context.Context, lis net.Listener) error {
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		sv.grpc.GracefulStop()
		sv.svc.Close()
	}()

	err := sv.grpc.Serve(lis)
	<-stopped
	if err != nil && ctx.Err() == nil {
		return err
	}
	// Serve returns nil after a GracefulStop, and a shutdown asked for is not
	// a failure to report.
	return nil
}
