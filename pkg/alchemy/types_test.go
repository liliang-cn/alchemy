package alchemy

import "testing"

// A graph's counts split on this one question, so the answer belongs to the
// Producer rather than to each stage that has to ask it.
func TestDeterministicSaysWhetherAnythingWasInferred(t *testing.T) {
	for _, tc := range []struct {
		producer Producer
		want     bool
	}{
		{ProducerDDL, true},
		{ProducerGraphImport, true},
		{ProducerTabular, false},
		{ProducerLLMExtract, false},
	} {
		if got := tc.producer.Deterministic(); got != tc.want {
			t.Errorf("%q.Deterministic() = %v, want %v", tc.producer, got, tc.want)
		}
	}
}

// An unknown producer must not count as deterministic. A new producer added
// without a decision here would otherwise arrive claiming a machine read it.
func TestAnUnknownProducerIsNotDeterministic(t *testing.T) {
	if Producer("something-new").Deterministic() {
		t.Error(`Producer("something-new").Deterministic() = true; want false`)
	}
}
