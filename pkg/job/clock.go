package job

import (
	"sync"
	"time"
)

// Clock is time as a value the store is given rather than time as a thing it
// reaches for. Every deadline in this package — leases, the two review
// expiries, the retention of finished work — is a comparison against Now, and
// a test that proved one of them by sleeping would be a test that takes a
// minute to say something a test could say instantly, and that fails on a
// loaded machine for reasons unrelated to the code.
type Clock interface {
	Now() time.Time
}

// systemClock is the default: the wall clock, and the only place in the
// package that calls time.Now.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// ManualClock is time a test moves by hand. It is exported because the store
// is a dependency of other packages, and a service test that has to invent its
// own fake clock will invent one that disagrees with this one about whether
// deadlines are inclusive.
//
// It is mutex-guarded because the race test advances time while workers read
// it, which is the interesting case and would otherwise be the one the race
// detector complains about instead of the code under test.
type ManualClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewManualClock(start time.Time) *ManualClock { return &ManualClock{now: start} }

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward. Nothing moves it back: a store that had to
// cope with time running backwards would need to defend every deadline
// comparison, and the only caller who could do that is a test being unhelpful.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
