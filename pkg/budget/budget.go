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

import "context"

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
}

// Known gap, recorded here rather than in a report nobody will find again: a
// Lease has no liveness. §8.3 requires leases with heartbeats, and a node that
// dies holding a slot must not shrink the cluster's budget forever. The local
// implementation cannot exercise a heartbeat — the slot dies with the process
// that held it — so one was not added speculatively. A shared-store
// implementation will need Heartbeat(ctx) error and a TTL, and adding a method
// to this interface later breaks every implementor: that cost is known and
// accepted, because the alternative is an untested method shaping a contract
// around a store nobody has written yet.
