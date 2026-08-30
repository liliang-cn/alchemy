package cortexdb

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// §7.3: a graph that contradicts itself is worse than no graph, because an
// agent reading it will answer from whichever edge it happened to traverse —
// confidently, with a citation. CortexDB is the store those agents read, so
// this is the last gate before the citation exists.
//
// Refusing has to mean nothing at all was written, not even the run marker: a
// held import that left a marker behind looks, to the next operator, like a
// load that crashed and should be retried.
func TestHeldResultLeavesNothingBehind(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-H"})
	before := countNodes(t, l)

	prov := alchemy.Provenance{Source: "a.pdf", Chunk: 0, Producer: alchemy.ProducerLLMExtract}
	res := fixture()
	res.Conflicts = []alchemy.Conflict{{
		Kind: alchemy.ConflictRelationDirection, Subject: "e1 USES e2",
		Detail: "the schema says one way and the PDF the other",
		Left:   alchemy.Claim{Statement: "e1 uses e2", Provenance: prov},
		Right:  alchemy.Claim{Statement: "e2 uses e1", Provenance: prov},
	}}

	if _, err := l.Load(context.Background(), res); !errors.Is(err, ErrHeld) {
		t.Fatalf("Load of a held result: err = %v, want ErrHeld", err)
	}
	if got := countNodes(t, l); got != before {
		t.Fatalf("%d nodes written for a held result, want none", got-before)
	}
}

// The same result with the conflict answered loads normally. The connector
// asks pkg/review whether a conflict is open rather than deciding for itself,
// so that "held" cannot come to mean two things.
func TestAnsweredConflictLoads(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-H2"})
	res := fixture()
	c := alchemy.Conflict{Kind: alchemy.ConflictEntityType, Subject: "e1"}
	c.Left.Provenance.ReviewedBy = "ada"
	res.Conflicts = []alchemy.Conflict{c}

	rep, err := l.Load(context.Background(), res)
	if err != nil {
		t.Fatalf("Load of an answered conflict: %v", err)
	}
	if rep.Entities != 3 {
		t.Fatalf("loaded %d entities, want 3", rep.Entities)
	}
}
