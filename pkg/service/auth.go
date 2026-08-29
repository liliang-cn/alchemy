package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Authentication is §5's scope item, and it is an interceptor rather than a
// line at the top of each method for one reason: an RPC added later gets it
// without anybody remembering to. A per-method check is a check somebody
// forgets exactly once, on the method that matters.
//
// The scheme is a bearer token in the `authorization` metadata header,
// compared in constant time. It is deliberately the simplest real thing: a
// buyer terminating TLS in front of this and issuing one token per client gets
// something defensible, and anything more — mTLS, JWT, an identity provider —
// is an interceptor swap rather than a change to any method here.
type tokens struct {
	// The digest rather than the token: the comparison is over fixed-width
	// bytes, so it does not leak the token's length, and a heap dump of a
	// running service does not hand out the credential.
	want [sha256.Size]byte
}

func newTokens(token string) *tokens {
	return &tokens{want: sha256.Sum256([]byte(token))}
}

// authenticate is the whole check. It is written to be boring: every failure
// returns the same code and the same sentence, because a message that
// distinguished "no header" from "wrong token" would be an oracle telling a
// prober which half they got right.
func (t *tokens) authenticate(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return errUnauthenticated
	}
	for _, v := range md.Get("authorization") {
		presented, ok := bearer(v)
		if !ok {
			continue
		}
		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(got[:], t.want[:]) == 1 {
			return nil
		}
	}
	return errUnauthenticated
}

var errUnauthenticated = status.Error(codes.Unauthenticated, "a valid bearer token is required")

// bearer pulls the credential out of an `authorization` value. The scheme is
// matched case-insensitively because the RFC says it is, and a client library
// that sends "bearer" is not the bug this function is looking for.
func bearer(value string) (string, bool) {
	const scheme = "bearer "
	if len(value) < len(scheme) || !strings.EqualFold(value[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(value[len(scheme):]), true
}

// UnaryInterceptor authenticates every unary call.
func (s *Server) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if err := s.tokens.authenticate(ctx); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// StreamInterceptor authenticates every streaming call, at the moment it is
// opened rather than per message. A stream whose credential is checked once is
// the right shape here: the connection is the thing that was authorised, and
// re-checking a token that cannot change mid-stream would be ceremony.
func (s *Server) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		if err := s.tokens.authenticate(ss.Context()); err != nil {
			return err
		}
		return next(srv, ss)
	}
}
