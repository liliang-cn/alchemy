package service_test

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The table is the service descriptor rather than a list somebody maintains.
//
// That is the point of the test. A hand-written list of RPCs is a list that is
// correct until the day an RPC is added, and the RPC added on that day is the
// one nobody remembers to authenticate. Walking the generated descriptor means
// a new method is in this test the moment it is in the proto, and it fails
// here before it ships without a check.
//
// The calls are made generically — an Empty in, an Empty out — because
// authentication runs before the request is ever unmarshalled. A method that
// let the call through would fail on the message type instead, which is a
// different error and still a failure of this test.
func TestEveryRPCRefusesAnUnauthenticatedCall(t *testing.T) {
	conn := connect(t, harness{})

	// Non-empty rather than a count. The guard is here because rpcs() reads
	// the descriptor by reflection and an empty slice would make every
	// assertion below vacuous — the test would pass loudest exactly when it
	// had stopped testing anything.
	//
	// It used to be `!= 8`, which is the hand-maintained list the paragraph
	// above argues against, spelled as a number instead of as names. When the
	// proto gained ListFindings, Decide and Assert it became `11 != 8` and
	// t.Fatalf aborted before a single subtest ran — so for that window NO rpc
	// was having its unauthenticated call checked, including the three new
	// ones, and the suite reported one red test rather than eleven unchecked
	// methods. A literal that turns a whole security check off when it goes
	// stale is worse than no literal.
	methods := rpcs()
	if len(methods) == 0 {
		t.Fatal("the service descriptor lists no RPCs; every assertion below would be vacuous")
	}
	t.Logf("checking %d RPCs against %d kinds of bad credential", len(methods), 6)

	credentials := []struct {
		name string
		ctx  func(context.Context) context.Context
	}{
		{"no metadata at all", func(ctx context.Context) context.Context { return ctx }},
		{"no authorization header", func(ctx context.Context) context.Context {
			return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-other", "value"))
		}},
		{"an empty authorization header", func(ctx context.Context) context.Context {
			return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", ""))
		}},
		{"a token with no scheme", func(ctx context.Context) context.Context {
			return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", testToken))
		}},
		{"the wrong token", func(ctx context.Context) context.Context {
			return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer not-the-token"))
		}},
		{"a token that is a prefix of the right one", func(ctx context.Context) context.Context {
			return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+testToken[:4]))
		}},
	}

	for _, m := range methods {
		for _, cred := range credentials {
			t.Run(m.name+"/"+cred.name, func(t *testing.T) {
				err := m.call(cred.ctx(context.Background()), conn)
				if got := status.Code(err); got != codes.Unauthenticated {
					t.Errorf("%s with %s: code = %v, want Unauthenticated (err %v)", m.name, cred.name, got, err)
				}
			})
		}
	}
}

// The mirror of the table above: a valid token must not be refused. Without
// it, a server that rejected everything would pass the test that matters most.
func TestEveryRPCAcceptsAValidToken(t *testing.T) {
	conn := connect(t, harness{})
	for _, m := range rpcs() {
		t.Run(m.name, func(t *testing.T) {
			err := m.call(authed(context.Background()), conn)
			if got := status.Code(err); got == codes.Unauthenticated {
				t.Errorf("%s refused a valid token: %v", m.name, err)
			}
		})
	}
}

// New refuses to start with no token configured, because the failure it
// prevents is silent: a service running unauthenticated looks exactly like one
// running correctly until somebody finds it.
func TestNewRefusesToStartWithoutAToken(t *testing.T) {
	_, err := service.New(service.Config{Runner: harness{}, Spool: t.TempDir()})
	if err == nil {
		t.Fatal("a server with no token started; authentication being off by omission is the mistake nobody notices")
	}
}

// rpc is one entry of the generated table.
type rpc struct {
	name string
	call func(context.Context, *grpc.ClientConn) error
}

// rpcs derives the table from the generated service descriptor.
func rpcs() []rpc {
	var out []rpc
	for _, m := range alchemyv1.Alchemy_ServiceDesc.Methods {
		full := "/" + alchemyv1.Alchemy_ServiceDesc.ServiceName + "/" + m.MethodName
		out = append(out, rpc{m.MethodName, func(ctx context.Context, conn *grpc.ClientConn) error {
			return conn.Invoke(ctx, full, &emptypb.Empty{}, &emptypb.Empty{})
		}})
	}
	for _, sd := range alchemyv1.Alchemy_ServiceDesc.Streams {
		full := "/" + alchemyv1.Alchemy_ServiceDesc.ServiceName + "/" + sd.StreamName
		desc := &grpc.StreamDesc{
			StreamName:    sd.StreamName,
			ClientStreams: sd.ClientStreams,
			ServerStreams: sd.ServerStreams,
		}
		out = append(out, rpc{sd.StreamName, func(ctx context.Context, conn *grpc.ClientConn) error {
			stream, err := conn.NewStream(ctx, desc, full)
			if err != nil {
				return err
			}
			// One request message and then a half close, whichever direction
			// streams: a server-streaming RPC waits for its single request,
			// and a client-streaming one needs an end to know it has them all.
			//
			// SendMsg's error is deliberately dropped rather than returned.
			// gRPC's contract is that a send onto a stream the server has
			// already ended returns io.EOF and "the status is obtained by
			// calling RecvMsg" — so returning it here reports a bare EOF
			// instead of the refusal, and does so only when the server was
			// fast enough to reject before the client finished sending. That
			// is a race on the one table where a flaky signal is least
			// affordable: a rejection this test failed to see reads the same
			// as a rejection that never happened.
			_ = stream.SendMsg(&emptypb.Empty{})
			_ = stream.CloseSend()
			// The refusal arrives on the first receive: a stream is opened
			// optimistically and the interceptor's error is the first thing
			// the server has to say about it.
			return stream.RecvMsg(&emptypb.Empty{})
		}})
	}
	return out
}
