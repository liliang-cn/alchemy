package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/cache"
	"github.com/liliang-cn/alchemy/pkg/gateway"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/runner"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// server is the two halves of the running process: the transport, and the
// service behind it. They are kept together because stopping is two calls in
// one order — stop accepting, then stop the work — and a caller holding only
// one of them cannot get that order right.
type server struct {
	grpc *grpc.Server
	svc  *service.Server
	// http is DESIGN.md §6's gateway, nil when no address was configured. It
	// is held here rather than beside the process because shutting down is now
	// three calls in one order and the order matters more than before: the
	// gateway is a *client* of the gRPC server, so it has to stop before the
	// thing it dials.
	http *http.Server
	// conn is the gateway's connection to our own gRPC server.
	conn *grpc.ClientConn
	// stopGateway releases the registration the gateway made against its
	// context.
	stopGateway context.CancelFunc
}

// build assembles the program. It is separate from main and from listening so
// that a test can start the whole thing on a port the operating system picks
// and stop it again.
func build(s settings, token string, rules []review.Rule) (*server, error) {
	b, err := modelBudget(s)
	if err != nil {
		return nil, err
	}
	// The rule set is the runner's rather than the service's because it is
	// policy about extraction, and it reaches a job the same way the budget
	// and the cache do: as configuration the process was started with, in
	// front of whatever the job itself supplied.
	run, err := runner.New(runner.Config{Factory: modelFactory{}, Budget: b, Cache: extractCache(s), Rules: rules})
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

// attachGateway builds the REST surface in front of our own gRPC server.
//
// It dials the gRPC server rather than reaching into the service, and that is
// the point rather than an accident. §6 says the gateway "is a translation,
// never a second source of truth about what the service does"; a gateway
// holding a *service.Server would be able to call methods no RPC exposes, skip
// the interceptors that authenticate, and drift from the wire contract without
// anything failing to compile. Going through the front door costs a loopback
// hop per request and buys the guarantee that a buyer curling this and a buyer
// using gRPC are talking to the same service.
//
// It carries no credential of its own. The caller's Authorization header is
// forwarded as gRPC metadata by grpc-gateway, so the token a curl user
// presents is the token pkg/service checks — there is no service account here
// to leak, and no way to reach the service through the gateway that a gRPC
// client could not reach directly.
//
// The address comes from the listener rather than from the settings, which is
// not a detail: -addr 127.0.0.1:0 is a legal configuration and is what every
// test uses, and a gateway that dialled the *configured* address would then be
// pointed at port zero — a request that hangs rather than fails, which is the
// worst of the available wrong answers.
func (sv *server) attachGateway(addr net.Addr) error {
	conn, err := grpc.NewClient(dialTarget(addr), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("alchemy: the gateway cannot reach %s: %w", addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	handler, err := gateway.New(ctx, conn)
	if err != nil {
		cancel()
		conn.Close()
		return fmt.Errorf("alchemy: building the gateway: %w", err)
	}
	sv.conn = conn
	sv.stopGateway = cancel
	// ReadHeaderTimeout and no write timeout: a WatchJob stream is minutes
	// long and a write deadline would cut it, while a client that opens a
	// connection and never sends a request line is the cheap denial of service
	// every net/http server has to refuse for itself.
	sv.http = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	return nil
}

// dialTarget turns a listening address into one a client can dial. A server
// listening on :7431 or on 0.0.0.0:7431 or on [::] is reachable at
// 127.0.0.1:7431, and a gateway that dialled the wildcard verbatim would fail
// on the platforms where that is not routable — a startup failure that reads
// as a bug in the gateway rather than as an address it was never meant to dial.
func dialTarget(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr.String()
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
// The order is now three deep and every step of it is load-bearing. The
// gateway first: it is a client of the gRPC server, so stopping it after would
// leave HTTP requests in flight with nothing to translate to, and the buyer
// evaluating the product would meet the shutdown as a 500. Then GracefulStop:
// it stops accepting immediately and lets the calls already in flight finish,
// so a client mid-upload gets an answer instead of a reset connection. Then
// service.Close: it cancels the context every running job hangs off, which is
// how a job learns the process is going away, and waits for them. Doing that
// one earlier would cancel a job whose caller is still being served, and the
// caller would be told their import failed by a service that was about to exit
// anyway.
//
// httpLis is nil when no -http-addr was given, which is the default. A nil
// listener is not an error here: it is what "the gateway is off" looks like at
// the point where somebody would otherwise have to remember to check.
func (sv *server) serve(ctx context.Context, lis, httpLis net.Listener) error {
	if httpLis != nil {
		// Built here rather than in build() because it needs the address the
		// gRPC server actually got, which only exists once something is
		// listening on it.
		if err := sv.attachGateway(lis.Addr()); err != nil {
			return err
		}
	}
	gateways := sv.serveGateway(httpLis)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		sv.stopHTTP()
		sv.grpc.GracefulStop()
		sv.svc.Close()
	}()

	err := sv.grpc.Serve(lis)
	<-stopped
	<-gateways
	if err != nil && ctx.Err() == nil {
		return err
	}
	// Serve returns nil after a GracefulStop, and a shutdown asked for is not
	// a failure to report.
	return nil
}

// serveGateway starts the HTTP surface, if there is one, and hands back a
// channel that closes when it has stopped. An already-closed channel when
// there is nothing to serve keeps the caller from having to know which
// configuration it is in.
func (sv *server) serveGateway(lis net.Listener) <-chan struct{} {
	done := make(chan struct{})
	if sv.http == nil || lis == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		// ErrServerClosed is the shutdown that was asked for. Anything else is
		// worth a line on stderr rather than silence, because a gateway that
		// died while gRPC kept serving is a product that is half up — and the
		// half that is down is the one the buyer was told to curl.
		if err := sv.http.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "alchemy: the gateway stopped: %v\n", err)
		}
	}()
	return done
}

// stopHTTP drains the gateway and lets go of its connection.
//
// Shutdown rather than Close: an HTTP request in flight is a gRPC call in
// flight, and cutting it would turn a graceful stop into a truncated upload.
// The timeout is what stops a WatchJob stream — which by design does not end
// until the job does — from holding the process open forever.
func (sv *server) stopHTTP() {
	if sv.http == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayDrain)
	defer cancel()
	_ = sv.http.Shutdown(ctx)
	if sv.stopGateway != nil {
		sv.stopGateway()
	}
	if sv.conn != nil {
		_ = sv.conn.Close()
	}
}

// gatewayDrain is how long an in-flight HTTP request has to finish. It is
// short because the requests that outlive it are the streams, and a stream is
// meant to be interrupted by a shutdown — its client learns the job's state by
// asking again, which is exactly what it would do after any disconnection.
const gatewayDrain = 5 * time.Second
