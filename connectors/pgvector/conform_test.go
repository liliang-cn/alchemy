package pgvector

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/refusable"
	check "github.com/liliang-cn/alchemy/pkg/preflight"
)

// The four connectors were written without sight of each other and each
// arrived at a different subset of one set of refusals. Where they differed,
// they differed by omission rather than by opinion, and every omission is a
// silent overwrite: a record written where two were counted.
//
// pkg/preflight is where the shared rule now lives, and this is the evidence
// that this store asks it. Asserted through Load rather than through the
// internal check, because what a buyer needs is that nothing is written — so
// the assertion is on the store as well as on the answer.
func TestEveryResultTheContractRefusesIsRefusedHere(t *testing.T) {
	for _, c := range refusable.Cases(4) {
		t.Run(c.Name, func(t *testing.T) {
			if err := check.Refuse(c.Result); err == nil {
				t.Fatalf("the fixture is not refusable any more: %s", c.Why)
			}
			l := newFixture(t).open(t, Config{})
			if _, err := l.Load(context.Background(), c.Result, LoadOptions{}); err == nil {
				t.Fatalf("accepted; %s", c.Why)
			}
		})
	}
}

// And the clean graph still loads, which is what makes the refusals above
// about the defect rather than about the fixture.
func TestTheCleanFixtureIsAccepted(t *testing.T) {
	l := newFixture(t).open(t, Config{})
	if _, err := l.Load(context.Background(), refusable.Clean(4), LoadOptions{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
