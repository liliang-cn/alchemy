package qdrant

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/recallconform"
)

// The read side of the same argument sinkconform makes about the write side:
// six connectors were written apart, and a suite only this connector's author
// runs would let the eight primitives drift back into six answers.
func TestRecallConformance(t *testing.T) {
	recallconform.Run(t, func(t *testing.T) recallconform.Store {
		return newFixture(t).open(t, Config{})
	})
}
