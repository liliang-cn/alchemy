package qdrant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func conflicted(res alchemy.Result, reviewer string) alchemy.Result {
	c := alchemy.Conflict{
		Kind:    alchemy.ConflictRelationDirection,
		Subject: "SuperAI -[USES]-> CortexDB",
		Detail:  "architecture.pdf says SuperAI uses CortexDB; schema.sql says the reverse",
		Left:    alchemy.Claim{Statement: "SuperAI USES CortexDB", Provenance: prov(1)},
		Right:   alchemy.Claim{Statement: "CortexDB USES SuperAI", Provenance: prov(0)},
	}
	c.Left.Provenance.ReviewedBy = reviewer
	c.Right.Provenance.ReviewedBy = reviewer
	res.Conflicts = append(res.Conflicts, c)
	res.Counts.Conflicts = 1
	return res
}

// §7.3: a job that finds a conflict does not finish, "whether or not the
// caller asked for review mode", because "a graph that contradicts itself is
// worse than no graph". The connector is the step at which that stops being a
// job somebody is watching and becomes a store somebody queries.
//
// The strong form of the assertion is that the collection is not even created:
// a refusal has to leave the server exactly as it found it, and a connector
// that created a collection before deciding whether it would write anything
// into it would leave a buyer with an empty artefact they did not ask for.
func TestAHeldResultIsRefusedAndNothingIsWritten(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()

	_, err := l.Load(ctx, conflicted(smallResult(8), ""), LoadOptions{})
	var he *HeldError
	if !errors.As(err, &he) {
		t.Fatalf("err = %v, want *HeldError", err)
	}
	if len(he.Conflicts) != 1 {
		t.Errorf("HeldError carries %d conflicts, want 1", len(he.Conflicts))
	}
	if !strings.Contains(err.Error(), "SuperAI -[USES]-> CortexDB") {
		t.Errorf("the refusal has to name what is unanswered, got: %v", err)
	}
	if _, err := l.CollectionDimension(ctx); !errors.Is(err, ErrNoCollection) {
		t.Errorf("a held result created the collection: %v", err)
	}
}

// A conflict a person answered is not held, and the answer is worth keeping:
// §5c says review adds to provenance rather than overwriting it, so a store
// that dropped the answered conflicts would leave a reader unable to tell a
// graph nobody questioned from one somebody settled.
func TestAnAnsweredConflictLoadsAndIsKept(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	if _, err := l.Load(ctx, conflicted(smallResult(8), "ada@example.com"), LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	loads, err := l.Loads(ctx)
	if err != nil || len(loads) != 1 {
		t.Fatalf("Loads = %v (err %v), want one load", loads, err)
	}
	if loads[0].Counts.Conflicts != 1 {
		t.Errorf("counts.conflicts = %d, want 1: §5's numbers travel with the graph", loads[0].Counts.Conflicts)
	}
	kept, err := l.Findings(ctx, loads[0].ID)
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(kept.Conflicts) != 1 || kept.Conflicts[0].Left.Provenance.ReviewedBy != "ada@example.com" {
		t.Errorf("stored conflicts = %+v, want one naming ada@example.com", kept.Conflicts)
	}
}

// Violations are on the other side of §7.3's line: attributable, excludable,
// and the rest of the graph is usable without them. A connector that refused
// them would be refusing the graph they came with.
func TestViolationsDoNotHoldALoadAndKeepTheirProvenance(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	res.Violations = []alchemy.Violation{{
		Kind: alchemy.ViolationUnknownEntityType, Detail: "type Widget is not in sds@3",
		Subject: "Widget-1", Provenance: prov(0),
	}}
	res.Counts.Violations = 1
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	recs, err := l.Records(ctx, Filter{Kinds: []string{"violation"}}, 0)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(recs.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(recs.Violations))
	}
	got := recs.Violations[0]
	if got.Subject != "Widget-1" || got.Provenance.Producer != alchemy.ProducerLLMExtract || got.Provenance.Chunk != 0 {
		t.Errorf("violation = %+v, want Widget-1, llm-extract, chunk 0: a violation is returned with the chunk it came from", got)
	}
}

// A dangling relation is a violation, not a load failure — and in a store with
// no joins it has to be marked, because a reader who followed the edge and
// found no node would otherwise blame the store.
func TestADanglingRelationLoadsAndIsMarkedAsDangling(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	res.Relations = append(res.Relations, alchemy.Relation{
		From: "SuperAI", To: "GhostService", Type: "USES", Provenance: prov(1),
	})
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	n, err := l.Count(ctx, Filter{Kinds: []string{"relation"}})
	if err != nil || n != 2 {
		t.Fatalf("relations = %d (err %v), want 2", n, err)
	}
	dangling, err := l.Count(ctx, Filter{Kinds: []string{"relation"}, Dangling: true})
	if err != nil || dangling != 1 {
		t.Errorf("dangling relations = %d (err %v), want 1", dangling, err)
	}
}
