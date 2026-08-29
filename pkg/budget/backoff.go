package budget

import (
	"math/rand/v2"
	"time"
)

// Default backoff policy. Half a second is long enough that the endpoint's own
// window has moved and short enough that a single spurious 429 does not stall a
// job; thirty seconds is the cap because a wait longer than that is better
// spent failing the job so an operator sees it.
const (
	defaultBase   = 500 * time.Millisecond
	defaultMax    = 30 * time.Second
	defaultJitter = 0.5
)

// Backoff is how long an endpoint stays closed after it rate-limits a call.
//
// It is a property of the endpoint, not of a caller: §8.2 says backoff is
// coordinated through the same lease rather than chosen independently by each
// node, because a cluster that each picks its own delay after a 429 is a
// cluster attacking its own dependency. The zero value is a usable policy.
type Backoff struct {
	// Base is the first delay; each further round doubles it.
	Base time.Duration
	// Max is the ceiling. It bounds the wait even after a hundred rounds, and
	// it bounds an endpoint that asks for an implausible Retry-After.
	Max time.Duration
	// Jitter is the fraction of each delay that is random, in [0,1]. 0.5 — the
	// default — means a delay is never shorter than half its nominal value and
	// never longer than the whole of it. 1 is full jitter.
	//
	// The herd this jitter breaks up is not the one inside a single budget:
	// there, every waiter is already bounded by Limit, so at most Limit calls
	// can leave at once no matter how synchronised they are. It is the
	// correlation between separate budgets — several clusters, several jobs,
	// and the endpoint's own window boundary — that turns a shared deadline
	// into a spike, and an unjittered exponential schedule keeps hitting the
	// same boundary round after round.
	Jitter float64
}

func (b Backoff) normalised() Backoff {
	if b.Base <= 0 {
		b.Base = defaultBase
	}
	if b.Max <= 0 {
		b.Max = defaultMax
	}
	if b.Max < b.Base {
		b.Max = b.Base
	}
	// A jitter outside [0,1] is a misconfiguration; clamping it keeps a typo
	// from producing a negative wait or one over the cap.
	if b.Jitter < 0 {
		b.Jitter = 0
	}
	if b.Jitter > 1 {
		b.Jitter = 1
	}
	return b
}

// Delay is how long round `attempt` waits, given a draw in [0,1). The draw is a
// parameter rather than a package-level source so a test can name the number it
// asserts against instead of sleeping to find out.
func (b Backoff) Delay(attempt int, draw func() float64) time.Duration {
	p := b.normalised()
	if attempt < 1 {
		attempt = 1
	}
	d := p.Base
	// Shifting is done on the duration with an overflow guard rather than by
	// computing 2^attempt: attempt is unbounded, and 1<<64 is 0, which would
	// turn the hundredth rate limit into no backoff at all.
	for i := 1; i < attempt && d < p.Max; i++ {
		if d > p.Max/2 {
			d = p.Max
			break
		}
		d *= 2
	}
	if d > p.Max {
		d = p.Max
	}
	if p.Jitter == 0 || draw == nil {
		return d
	}
	x := draw()
	if x < 0 {
		x = 0
	} else if x > 1 {
		x = 1
	}
	fixed := float64(d) * (1 - p.Jitter)
	return time.Duration(fixed + float64(d)*p.Jitter*x)
}

// Clock is the time the budget waits against. It is injected so a test can move
// a backoff deadline forward instead of sleeping through it; a test that proves
// a delay by sleeping proves only that the machine was busy.
type Clock interface {
	Now() time.Time
	// NewTimer returns a timer that fires once after d. It is a timer rather
	// than a bare channel so an abandoned wait can Stop it: a cancelled caller
	// leaving a thirty-second timer behind is a leak per rate-limited call.
	NewTimer(d time.Duration) Timer
}

// Timer is one pending wait.
type Timer interface {
	C() <-chan time.Time
	Stop()
}

// SystemClock is the real clock, and the default.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) NewTimer(d time.Duration) Timer { return &systemTimer{t: time.NewTimer(d)} }

type systemTimer struct{ t *time.Timer }

func (t *systemTimer) C() <-chan time.Time { return t.t.C }
func (t *systemTimer) Stop()               { t.t.Stop() }

// defaultDraw is the production source of jitter. math/rand/v2's top-level
// functions are safe for concurrent use and seeded per process, which is what
// decorrelating two clusters needs.
func defaultDraw() float64 { return rand.Float64() }
