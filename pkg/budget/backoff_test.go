package budget_test

import (
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/budget"
)

// fixedRand is the injected randomness: every jitter assertion below names the
// draw it is made against, so a jittered delay is still an exact number.
func fixedRand(v float64) func() float64 { return func() float64 { return v } }

func TestDelayDoublesUntilItHitsTheCap(t *testing.T) {
	p := budget.Backoff{Base: time.Second, Max: 8 * time.Second, Jitter: 0}
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 8 * time.Second},
		{40, 8 * time.Second},
		{1 << 20, 8 * time.Second},
	} {
		if got := p.Delay(tc.attempt, fixedRand(1)); got != tc.want {
			t.Errorf("Delay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestJitterIsAFractionOfTheDelayAndNeverExceedsIt(t *testing.T) {
	p := budget.Backoff{Base: 4 * time.Second, Max: time.Minute, Jitter: 0.5}

	// The draw is the only source of variation, so both ends are exact.
	if got, want := p.Delay(1, fixedRand(1)), 4*time.Second; got != want {
		t.Errorf("Delay with draw 1.0 = %v, want the full %v", got, want)
	}
	if got, want := p.Delay(1, fixedRand(0)), 2*time.Second; got != want {
		t.Errorf("Delay with draw 0.0 = %v, want %v — half of it is fixed", got, want)
	}
	if got, want := p.Delay(1, fixedRand(0.5)), 3*time.Second; got != want {
		t.Errorf("Delay with draw 0.5 = %v, want %v", got, want)
	}

	// A jitter outside [0,1] is a misconfiguration, not a licence to wait
	// negative time or twice the cap.
	if got, want := (budget.Backoff{Base: time.Second, Max: time.Minute, Jitter: 5}).Delay(1, fixedRand(0)), time.Duration(0); got < want {
		t.Errorf("Delay with Jitter 5 = %v, want a non-negative delay", got)
	}
	if got, want := (budget.Backoff{Base: time.Second, Max: time.Minute, Jitter: -3}).Delay(1, fixedRand(0)), time.Second; got != want {
		t.Errorf("Delay with a negative Jitter = %v, want the unjittered %v", got, want)
	}
}

func TestZeroBackoffStillBacksOff(t *testing.T) {
	var p budget.Backoff
	d := p.Delay(1, fixedRand(1))
	if d <= 0 {
		t.Fatalf("the zero Backoff produced %v: a budget with no declared policy must still wait", d)
	}
	capped := p.Delay(64, fixedRand(1))
	if capped <= 0 || capped < d {
		t.Fatalf("Delay(64) = %v, want a bounded value at least the first delay %v", capped, d)
	}
	if got := p.Delay(64, fixedRand(1)); got != capped {
		t.Fatalf("the cap is not stable: %v then %v", capped, got)
	}
}

func TestDelayIsAlwaysBoundedByMax(t *testing.T) {
	p := budget.Backoff{Base: time.Second, Max: 10 * time.Second, Jitter: 1}
	for _, draw := range []float64{0, 0.001, 0.5, 0.999, 1} {
		for attempt := 1; attempt < 200; attempt++ {
			d := p.Delay(attempt, fixedRand(draw))
			if d < 0 || d > p.Max {
				t.Fatalf("Delay(%d) with draw %v = %v, outside [0, %v]", attempt, draw, d, p.Max)
			}
		}
	}
}
