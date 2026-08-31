package rdf

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/refusable"
	check "github.com/liliang-cn/alchemy/pkg/preflight"
)

// The shared refusals, asked here.
//
// pkg/preflight is where the rule lives and sink.Load is what asks it, so a
// connector written against the envelope gets them for free — which is most of
// why the envelope exists. This is the evidence that this one actually does:
// every result the contract refuses is refused before a single triple is
// written, and a store that quietly stopped asking fails a test rather than a
// customer's import.
//
// It is imported under a name that is not "preflight" because this package's
// own function already has that name — the same collision neo4j's has, and for
// the same reason: the connector called its check what it is, and so did the
// shared one.
func TestEveryResultTheContractRefusesIsRefusedHere(t *testing.T) {
	for _, c := range refusable.Cases(4) {
		t.Run(c.Name, func(t *testing.T) {
			if err := check.Refuse(c.Result); err == nil {
				t.Fatalf("the fixture is not refusable any more: %s", c.Why)
			}
		})
	}
}

// And the clean graph still passes this connector's own refusals, which is what
// makes them about the defect rather than about the fixture.
func TestTheCleanFixtureIsAcceptedByThisConnectorsOwnRefusals(t *testing.T) {
	if _, err := preflight(refusable.Clean(4), Options{RunID: "ld-clean"}); err != nil {
		t.Fatalf("preflight: %v", err)
	}
}

// A held result is this connector's own refusal and it stays one, because a
// caller matching on ErrHeld is matching on this package's contract. §7.3: a
// graph that contradicts itself is worse than no graph.
func TestAHeldResultIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	held := refusable.Cases(4)[0]
	if _, err := preflight(held.Result, Options{RunID: "ld-held"}); err == nil {
		t.Fatalf("accepted a held result; %s", held.Why)
	}
}

// A load with no name cannot be found again, and a generated one would make a
// retry after a crash indistinguishable from a second import.
func TestALoadWithNoNameIsRefused(t *testing.T) {
	// The result names a job, so Options.RunID is not needed: preflight falls
	// back to it, the same way neo4j's does.
	if _, err := preflight(refusable.Clean(4), Options{}); err != nil {
		t.Fatalf("a result that names its own job was refused: %v", err)
	}
	res := refusable.Clean(4)
	res.Job = ""
	if _, err := preflight(res, Options{}); err == nil {
		t.Fatal("a result naming no job and no RunID was accepted")
	}
}
