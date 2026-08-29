package budget

import (
	"errors"
	"fmt"
	"time"
)

// RateLimiter is implemented by an error that means "the endpoint refused this
// call because we are going too fast" — an HTTP 429, a provider quota error, a
// gRPC RESOURCE_EXHAUSTED.
//
// This is how a caller tells the budget that a 429 happened, and it is an
// interface rather than a list of message patterns on purpose. Matching on
// error text is a guess about wording that belongs to somebody else's SDK: it
// passes in the test that invented the string and fails silently in production
// the week the provider rewords its message — and a rate limit that stops being
// detected is exactly the retry storm §8.2 is about.
//
// An interface also costs an adapter nothing. These are ordinary method names
// on an ordinary struct, so a caller's error type can satisfy this without
// importing pkg/budget at all; the budget finds it with errors.As. Callers who
// do import this package can wrap ErrRateLimited or call TooFast instead.
type RateLimiter interface {
	error
	// RateLimited distinguishes a rate limit from every other failure of the
	// same error type, so one struct can carry a 429 and a 500.
	RateLimited() bool
	// RetryAfter is how long the endpoint itself asked us to wait — its
	// Retry-After header. Return 0 when it said nothing; the budget's own
	// exponential schedule is used then. The endpoint's number is better
	// information than any schedule we could compute, so it wins when it is
	// longer.
	RetryAfter() time.Duration
}

// ErrRateLimited is the sentinel for callers who would rather wrap than
// implement: fmt.Errorf("openai: %w", budget.ErrRateLimited) is recognised, and
// so is anything TooFast returns.
var ErrRateLimited = errors.New("model endpoint rate limited the call")

// TooFast marks cause as a rate limit, carrying the endpoint's own Retry-After
// when it sent one (0 when it did not). The cause survives errors.Is and
// errors.As, so nothing a caller wanted to report is lost by marking it.
func TooFast(cause error, retryAfter time.Duration) error {
	return &rateLimited{cause: cause, after: retryAfter}
}

type rateLimited struct {
	cause error
	after time.Duration
}

func (e *rateLimited) Error() string {
	if e.cause == nil {
		return ErrRateLimited.Error()
	}
	return e.cause.Error() + ": " + ErrRateLimited.Error()
}

func (e *rateLimited) RateLimited() bool         { return true }
func (e *rateLimited) RetryAfter() time.Duration { return e.after }

// Unwrap reports both the sentinel and the original cause, so errors.Is finds
// ErrRateLimited without the caller's own cause being swallowed to get it.
func (e *rateLimited) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrRateLimited}
	}
	return []error{ErrRateLimited, e.cause}
}

// IsRateLimit reports whether err says the endpoint refused the call for going
// too fast, and how long it asked us to wait (0 when it did not say).
//
// Both signalling routes are checked: an error implementing RateLimiter
// anywhere in its chain, and an error wrapping ErrRateLimited. Nothing else is
// consulted — in particular, never the message text.
func IsRateLimit(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var rl RateLimiter
	if errors.As(err, &rl) && rl.RateLimited() {
		return rl.RetryAfter(), true
	}
	if errors.Is(err, ErrRateLimited) {
		return 0, true
	}
	return 0, false
}

// errUnfinished marks a call that neither returned nor reported a rate limit —
// the panic path in the wrappers. It is unexported because a caller never needs
// to construct one: it exists so a model that panicked is not recorded as a
// healthy call proving the endpoint recovered.
var errUnfinished = fmt.Errorf("budget: the model call did not return")
