package budget

import (
	"context"
	"errors"
	"time"
)

// beatsPerTTL is how many renewals a lease gets inside one TTL.
//
// Three, not one. A heartbeat that fires exactly at the deadline loses the slot
// to a single dropped packet, and losing a slot mid-call is a worse failure
// than the one the TTL fixes: a ninety-second OCR call would start failing on a
// budget that used to work. Three means two consecutive renewals have to fail
// before anything is reclaimed, which is the difference between a TTL that
// detects a dead node and one that punishes a slow network.
const beatsPerTTL = 3

// Keepalive renews l in the background for as long as the guarded call runs,
// and returns the context that call must use.
//
// It is the only place a heartbeat is driven, so the rule lives here once
// rather than at every call site: hold a slot, keep saying so, and stop saying
// so the instant the work is done.
//
// The returned context is the one to hand to the model, and it is not merely
// the caller's own. When the store reports ErrLeaseExpired the slot is gone —
// somebody else is already using it — and the guarded call is cancelled with
// that error as its cause. A node that kept calling after losing its slot is
// exactly the overshoot §8.2 exists to prevent, arriving through the recovery
// path. Any other heartbeat failure is treated as a store that is briefly
// unreachable and is retried: the lease still has most of a TTL left, and
// cancelling a long call because one renewal timed out would reintroduce the
// failure the TTL was bought to avoid.
//
// For a lease with no liveness — Local's, whose slot dies with the process that
// holds it — Keepalive returns the caller's own context and a stop that does
// nothing. No goroutine, no timer, no allocation: the single-node path pays
// nothing for a distributed store it does not have.
//
// stop must be called exactly once, before Release. It waits for the renewing
// goroutine to finish, so no heartbeat can land after the slot has been given
// back and extend a lease nobody holds.
func Keepalive(ctx context.Context, l Lease) (context.Context, func()) {
	// No type assertion: liveness is part of what a Lease is, so there is no
	// "does this one renew?" for a caller to get wrong. A slot that cannot be
	// taken away answers zero and this returns immediately.
	ttl := l.TTL()
	if ttl <= 0 {
		return ctx, func() {}
	}
	interval := ttl / beatsPerTTL
	if interval <= 0 {
		interval = time.Millisecond
	}

	// WithCancelCause rather than WithCancel: a caller whose call was cut short
	// needs to tell "the cluster took my slot" from "my own deadline passed",
	// and those two produce the same ctx.Err().
	work, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-work.Done():
				return
			case <-t.C:
			}
			// The renewal is bounded by the interval it belongs to. A store
			// that has stopped answering must not park this goroutine past the
			// deadline it is supposed to be defending; the next tick tries
			// again, and there are beatsPerTTL of them per TTL.
			beat, endBeat := context.WithTimeout(work, interval)
			err := l.Heartbeat(beat)
			endBeat()
			if errors.Is(err, ErrLeaseExpired) {
				cancel(ErrLeaseExpired)
				return
			}
		}
	}()

	return work, func() {
		close(done)
		<-stopped
		// The cause is only read after an expiry, and cancel is idempotent, so
		// this is the ordinary teardown of the derived context rather than a
		// second opinion about why the call ended.
		cancel(context.Canceled)
	}
}
