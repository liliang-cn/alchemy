package model

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxBodyBytes is how much of a failing reply is kept as evidence.
//
// A provider that answers with an HTML error page — a load balancer's, usually,
// not the model's — will happily send a megabyte of it, and an error string is
// something that ends up in a log line, a job result and a support ticket. The
// first couple of kilobytes contain the provider's actual message in every
// format anyone ships; the rest is markup.
const maxBodyBytes = 2048

// APIError is a non-2xx answer from a model endpoint.
//
// It satisfies budget.RateLimiter without importing pkg/budget: those are
// ordinary methods on an ordinary struct, and the budget finds them with
// errors.As. That is the integration point §8.2 rests on — a 429 that the
// budget cannot see is an endpoint nobody backs off from, which is the retry
// storm. Nothing here matches on message text, and nothing should: matching a
// provider's wording is a guess that fails silently the week they reword it.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Model is Endpoint.Name — which of the three models a job supplied this
	// happened to, in the same spelling provenance and the budget use.
	Model string
	// URL is what was posted to. It never carries the API key: the key travels
	// in the Authorization header and nowhere else.
	URL string
	// Body is the provider's own explanation, truncated to maxBodyBytes and
	// with the API key redacted out of it.
	Body string
	// Truncated says Body is not the whole reply, so a reader can tell an
	// incomplete message from a terse one.
	Truncated bool

	// retryAfter is what the endpoint's Retry-After header asked for, 0 when
	// it asked for nothing. Unexported because RetryAfter() is the contract
	// budget.RateLimiter reads it through.
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "model %q at %s: %d %s", e.Model, e.URL, e.Status, http.StatusText(e.Status))
	if e.Body != "" {
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	if e.Truncated {
		fmt.Fprintf(&b, " (body truncated at %d bytes)", maxBodyBytes)
	}
	return b.String()
}

// RateLimited implements budget.RateLimiter. One struct carries a 429 and a
// 500, which is why the interface asks rather than assuming the type means it.
func (e *APIError) RateLimited() bool { return e.Status == http.StatusTooManyRequests }

// RetryAfter implements budget.RateLimiter: how long the endpoint itself asked
// us to wait, 0 when it said nothing. The endpoint's own number beats any
// schedule computed from this side, so it is passed through unmodified.
func (e *APIError) RetryAfter() time.Duration { return e.retryAfter }

// Retryable reports whether trying this call again could plausibly work.
//
// A 4xx is the caller's mistake — a wrong model name, a bad key, a malformed
// request — and will fail identically forever; paying for it twice is waste,
// not persistence. A 429 or a 5xx is the endpoint having a bad minute, and
// abandoning a corpus over a blip is worse. Note what this method is not: it
// does not retry and it does not say when to. §8.2 puts that decision in
// pkg/budget, where it can be coordinated across every worker on the endpoint;
// this only classifies what happened.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// TransportError is a call that never got an HTTP status: a refused
// connection, a dropped socket, a DNS failure, a client timeout.
type TransportError struct {
	Model string
	URL   string
	Err   error
	// retryable is decided where the failure happened, because only there is
	// it known whether the caller's own context had already been cancelled —
	// a job that is going away is not a call worth trying again.
	retryable bool
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("model %q at %s: %v", e.Model, e.URL, e.Err)
}

func (e *TransportError) Unwrap() error   { return e.Err }
func (e *TransportError) Retryable() bool { return e.retryable }

// retryable is the shape both error types share. It is unexported because a
// caller asks through the Retryable function rather than by asserting.
type retryable interface{ Retryable() bool }

// Retryable reports whether err is worth trying again, at a moment pkg/budget
// chooses. Anything that is not one of this package's call failures — a
// configuration error, a reply this client could not align — answers false:
// those do not get better by being repeated.
func Retryable(err error) bool {
	var r retryable
	if errors.As(err, &r) {
		return r.Retryable()
	}
	return false
}

// parseRetryAfter reads a Retry-After header in either legal form.
//
// Providers use both, so supporting only the seconds form means silently
// dropping the endpoint's own instruction from whichever half of them sends a
// date — and a dropped Retry-After is the budget guessing when the endpoint
// had already told it. A date that has already passed is 0, not a negative
// wait: 0 is how the budget knows to fall back to its own schedule.
func parseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(h); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// redact removes the API key from text a caller will read.
//
// An error travels — a log, a job result, a support ticket — and a gateway
// that echoes the Authorization header back inside its own error body is not
// hypothetical. The key is never a useful part of a diagnosis, so it is
// removed rather than trusted not to appear.
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}
