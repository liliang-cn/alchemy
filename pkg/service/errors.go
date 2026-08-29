package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/job"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// wireError maps an internal failure onto a gRPC code deliberately, because
// the code is the only part of an error most clients read. The mapping is the
// difference between a retry loop that works and one that hammers a service
// over something retrying will never fix:
//
//	NotFound           no such job, or one the retention sweep already dropped
//	InvalidArgument    the request could not be a valid request for anything
//	FailedPrecondition the request is fine but the job is in the wrong state
//	ResourceExhausted  §8.4's "try later" — admission control, retryable
//	Canceled           the caller went away, or cancelled the job
//
// FailedPrecondition and InvalidArgument are separated on purpose. A caller
// asking for the result of a job that is still running has sent a request that
// will be correct in a minute; a caller asking for the result of nothing has
// sent one that never will be. Collapsing them would tell a client to retry
// forever or to give up immediately, and each is wrong half the time.
func wireError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, job.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, job.ErrAtCapacity):
		// Retryable, and the message says so, because §8.4's whole argument is
		// that a rejected job is an operator's problem for a minute where an
		// accepted job that dies is their problem for an afternoon. A client
		// that cannot tell this apart from a permanent failure turns the good
		// outcome into the bad one.
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, job.ErrIllegalTransition), errors.Is(err, job.ErrLeaseLost):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, errInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, errWrongState):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, errTooLarge):
		// Not ResourceExhausted: nothing is exhausted and retrying the same
		// call will fail identically. The caller must ask a different way, and
		// the message names the RPC that works.
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// The service's own refusals. They are sentinels rather than pre-made status
// errors so that the mapping above stays the single place a code is chosen —
// a package that returns codes from twenty call sites is a package whose error
// contract nobody can read.
var (
	// errInvalid — the request could not be valid for any state of the world.
	errInvalid = errors.New("service: invalid request")
	// errWrongState — the request is well formed and the job is not ready for
	// it. §7.3 makes this a normal outcome rather than an error condition: a
	// held job is the design working.
	errWrongState = errors.New("service: job is not in a state that allows this")
	// errTooLarge — §8.4. A caller should never have to discover the 4MB limit
	// by receiving a truncation.
	errTooLarge = errors.New("service: result is too large for one message")
)

// invalid and wrongState wrap the sentinels with the sentence a caller acts on.
func invalid(format string, args ...any) error {
	return &wrapped{errInvalid, fmt.Sprintf(format, args...)}
}

func wrongState(format string, args ...any) error {
	return &wrapped{errWrongState, fmt.Sprintf(format, args...)}
}

func tooLarge(format string, args ...any) error {
	return &wrapped{errTooLarge, fmt.Sprintf(format, args...)}
}

// wrapped keeps the sentinel reachable by errors.Is while showing only the
// specific sentence, so an operator reading one log line is not told twice
// that the request was invalid.
type wrapped struct {
	kind error
	msg  string
}

func (w *wrapped) Error() string { return w.msg }
func (w *wrapped) Unwrap() error { return w.kind }
