package pgvector

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

// §7.3: a job that finds a conflict does not finish, "whether or not the caller
// asked for review mode", because "a graph that contradicts itself is worse
// than no graph". A connector is the exact step at which that stops being a
// job somebody is watching and becomes a store somebody queries, so it is the
// last place the rule can be enforced and the first place breaking it costs
// something.
func TestAHeldResultIsRefusedAndNothingIsWritten(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := conflicted(smallResult(8), "")

	_, err := l.Load(context.Background(), res, LoadOptions{})
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
	for _, table := range []string{"loads", "chunks", "entities", "relations"} {
		if n := f.count(t, table); n != 0 {
			t.Errorf("%s = %d, want 0: a held result must leave the store as it found it", table, n)
		}
	}
	// Not even the dimension: binding it would be this connector recording a
	// fact from a graph it just refused.
	if l.BoundDimension(context.Background()) != 0 {
		t.Error("a refused load bound the dimension")
	}
}

// A conflict a person answered is not held, and the answer is worth keeping:
// §5c says review adds to provenance rather than overwriting it, and a store
// that dropped the answered conflicts would leave a reader unable to tell a
// graph nobody questioned from one somebody settled.
func TestAnAnsweredConflictLoadsAndIsKept(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	res := conflicted(smallResult(8), "ada@example.com")
	if _, err := l.Load(ctx, res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	var reviewer string
	f.scalar(t, &reviewer, `SELECT conflicts->0->'left'->'provenance'->>'reviewed_by' FROM {s}.loads`)
	if reviewer != "ada@example.com" {
		t.Errorf("stored conflict names reviewer %q, want ada@example.com", reviewer)
	}
	var n int
	f.scalar(t, &n, `SELECT (counts->>'conflicts')::int FROM {s}.loads`)
	if n != 1 {
		t.Errorf("counts.conflicts = %d, want 1: §5's numbers travel with the graph", n)
	}
}

// Violations are on the other side of §7.3's line: attributable, excludable,
// and the rest of the graph is usable without them. A connector that refused
// them would be refusing the graph they came with.
func TestViolationsDoNotHoldALoad(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Violations = []alchemy.Violation{{
		Kind: alchemy.ViolationUnknownEntityType, Detail: "type Widget is not in sds@3",
		Subject: "Widget-1", Provenance: prov(0),
	}}
	res.Counts.Violations = 1
	if _, err := l.Load(context.Background(), res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := f.count(t, "violations"); n != 1 {
		t.Errorf("violations = %d, want 1", n)
	}
	var subject, producer string
	f.scalar(t, &subject, `SELECT subject FROM {s}.loaded_violations`)
	f.scalar(t, &producer, `SELECT prov_producer FROM {s}.loaded_violations`)
	if subject != "Widget-1" || producer != "llm-extract" {
		t.Errorf("violation = %q/%q, want Widget-1/llm-extract: a violation is returned with the chunk it came from", subject, producer)
	}
}

// A dangling relation is a violation, not a load failure. A foreign key here
// would have turned §7.3's "the rest of the graph is usable without it" into a
// refused import.
func TestADanglingRelationLoads(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Relations = append(res.Relations, alchemy.Relation{
		From: "SuperAI", To: "GhostService", Type: "USES", Provenance: prov(1),
	})
	if _, err := l.Load(context.Background(), res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := f.count(t, "relations"); n != 2 {
		t.Errorf("relations = %d, want 2", n)
	}
}
