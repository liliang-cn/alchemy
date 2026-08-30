package cortexdb

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/refusable"
	check "github.com/liliang-cn/alchemy/pkg/preflight"
)

// This connector already refused most of the shared list — a held job, an
// empty entity ID, two entities under one ID, two chunks under one index, two
// vector widths — which is why it is the one whose gaps were smallest and the
// one whose author had to write the most of it. It still missed the two that
// nobody caught: one chunk embedded twice, and a vector naming a chunk the
// result does not carry, which it dropped in silence.
//
// The list is one list now, and this is the evidence that this store asks it.
func TestEveryResultTheContractRefusesIsRefusedHere(t *testing.T) {
	for _, c := range refusable.Cases(4) {
		t.Run(c.Name, func(t *testing.T) {
			if err := check.Refuse(c.Result); err == nil {
				t.Fatalf("the fixture is not refusable any more: %s", c.Why)
			}
			if _, err := preflight(c.Result, Options{}); err == nil {
				t.Fatalf("accepted; %s", c.Why)
			}
		})
	}
}

func TestTheCleanFixtureIsAccepted(t *testing.T) {
	if _, err := preflight(refusable.Clean(4), Options{}); err != nil {
		t.Fatalf("preflight: %v", err)
	}
}
