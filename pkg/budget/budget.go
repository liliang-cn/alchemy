// Package budget bounds how many calls are in flight against one model
// endpoint, across every worker that shares the budget.
//
// §8.2 is the reason it exists. Extraction is a network call to a model the
// caller supplied, and that endpoint has a rate limit. Ten nodes each running
// "8 concurrent" is 80 in flight against an endpoint that permits 20 — the
// cluster's own success at scaling out is what triggers the 429s. So model
// concurrency is a cluster-wide budget, not a per-node setting: it is declared
// per endpoint, leased, and a node that cannot get a slot waits rather than
// tries.
//
// Nothing in the pipeline imports this package. A stage is budgeted by being
// handed a wrapped model — see WrapLLM, WrapEmbedder and WrapOCR — so that
// pkg/extract keeps knowing only about alchemy.LLM.
package budget

import (
	"context"
	"errors"
	"time"
)

// Budget is a bound on concurrent calls to model endpoints, one bound per
// endpoint: a job's embedder does not compete with its extraction model, and
// saturating one leaves the other free.
//
// The interface is deliberately lease-shaped rather than counter-shaped
// (§8.3 leases the same way it leases jobs), because the implementation that
// matters at scale lives in a shared store and has to survive the node that
// holds a slot dying. NewLocal is the in-process one, which is what a single
// node runs.
type Budget interface {
	// Acquire blocks until a slot for model is free and the endpoint is not in
	// backoff, or until ctx is done — in which case it returns ctx.Err() and no
	// lease. The model string is the endpoint key; pass alchemy.LLM.Name() so
	// the budget and the provenance agree on what an endpoint is.
	Acquire(ctx context.Context, model string) (Lease, error)
}

// Lease is one held slot. Release it exactly once; a second Release is a no-op
// rather than a second slot, because the alternative is a bound that quietly
// grows every time an error path runs twice.
type Lease interface {
	// Release returns the slot and reports how the guarded call ended: pass the
	// error the model returned, or nil for success.
	//
	// The error is what makes backoff coordinated instead of per-caller. A
	// rate-limit error (see IsRateLimit) puts the endpoint into backoff, so
	// every other worker on that endpoint waits too — that is the difference
	// between backing off and a retry storm. Any other error is neither
	// evidence of health nor of overload and changes nothing.
	Release(err error)

	// TTL is how long the slot survives with no heartbeat. Zero means the slot
	// cannot outlive the process holding it — nothing can reclaim it, so
	// nothing has to renew it, and Keepalive does no work at all.
	TTL() time.Duration

	// Heartbeat renews the slot for another TTL. It returns ErrLeaseExpired
	// when the slot is already gone: the holder was too slow or was thought
	// dead, and the store has given the slot to somebody else.
	//
	// This is on Lease rather than on a second interface a caller looks for,
	// and the reason is a bug this package shipped earlier the same day one
	// level up: pkg/embed asks an embedder for its usage through an optional
	// interface, WrapEmbedder implemented only the required half, and a real
	// run reported 258 tokens unwrapped and 0 wrapped — compiling, passing,
	// silently wrong. An optional method is one a wrapper drops by writing
	// nothing. A required one is a wrapper that does not compile.
	//
	// The cost was named when the gap was recorded and is paid here: every
	// implementor now has these two methods, including the in-process lease
	// where they mean "nothing can take this slot from me".
	Heartbeat(ctx context.Context) error
}

// LiveLease is the liveness half of a lease, and every Lease this package
// returns carries it.
//
// §8.3 asks for leases with heartbeats because a node that dies holding a slot
// must not shrink the cluster's budget forever. A slot in a shared store cannot
// tell a node that is working from one that is gone, so it expires, and the
// holder says "still here" by renewing it.
//
// It is a second interface rather than two more methods on Lease for a reason
// that is not compatibility: the two say different things. Lease is the
// permission to call; LiveLease is how that permission is kept alive against a
// store that will otherwise reclaim it. A budget with no store to reclaim from
// answers TTL with 0 — see Local's lease — and a caller that respects that
// answer does no work at all in the single-node case, which is the case the
// product ships in by default.
//
// The honest note that belongs beside it: making Heartbeat a method on Lease
// itself was considered and rejected here, not deferred. Every implementor
// would have to write a method whose only possible body is `return nil`, and
// the compiler would be enforcing ceremony rather than a contract. Keepalive
// asserts for this interface once, in the one place a lease is held across a
// model call.
// ErrLeaseExpired is what Heartbeat reports when the slot has already been
// reclaimed. It is distinguished from every other heartbeat failure on purpose:
// an unreachable store is a reason to try again, and a reclaimed slot is a
// reason to stop calling the endpoint, because the cluster has already counted
// that slot as free and given it to someone else.
var ErrLeaseExpired = errors.New("budget: the lease expired and the slot was reclaimed")
