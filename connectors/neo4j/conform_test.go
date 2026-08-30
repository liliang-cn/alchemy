package neo4j

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/refusable"
	check "github.com/liliang-cn/alchemy/pkg/preflight"
)

// The four connectors were written without sight of each other and each
// arrived at a different subset of one set of refusals. This one refused a held
// job and an entity with no ID at all, and did not refuse two entities under
// one ID or two chunks under one index — both of which its MERGEs turn into a
// silent overwrite, with the Report saying two where the graph holds one.
//
// pkg/preflight is where the shared rule now lives, and this is the evidence
// that it is asked. It is imported under a name that is not "preflight" because
// this package's own function already has that name, which is a fair sign of
// what happened: the connector called its check what it is, and so did the
// shared one.
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

// And the clean graph still passes, which is what makes the refusals above
// about the defect rather than about the fixture.
func TestTheCleanFixtureIsAccepted(t *testing.T) {
	if _, err := preflight(refusable.Clean(4), Options{}); err != nil {
		t.Fatalf("preflight: %v", err)
	}
}
