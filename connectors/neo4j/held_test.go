package neo4j

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// §7.3: a graph that contradicts itself is worse than no graph, because an
// agent reading it will answer from whichever edge it happened to traverse —
// confidently, with a citation. The service refuses to hand such a result
// over; this asserts that the connector refuses to take one, and that refusing
// means nothing at all was written, not even the run marker. A held import
// that left a marker behind would look, to the next operator, like a load that
// crashed and should be retried.
func TestHeldResultLeavesNothingBehind(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-H"})
	prov := alchemy.Provenance{Source: "a.pdf", Chunk: 3, Producer: alchemy.ProducerLLMExtract}
	res := fixture()
	res.Conflicts = []alchemy.Conflict{{
		Kind: alchemy.ConflictRelationDirection, Subject: "e1 USES e2",
		Detail: "the schema says one way and the PDF the other",
		Left:   alchemy.Claim{Statement: "e1 uses e2", Provenance: prov},
		Right:  alchemy.Claim{Statement: "e2 uses e1", Provenance: prov},
	}}

	_, err := l.Load(context.Background(), res)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("Load of a held result: err = %v, want ErrHeld", err)
	}
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+") RETURN count(n) AS n", nil)
	if recs[0]["n"] != int64(0) {
		t.Fatalf("%v nodes written for a held result, want none", recs[0]["n"])
	}
}

// The same result with the conflict answered loads normally. The connector
// asks pkg/review whether a conflict is open rather than deciding for itself,
// so that "held" cannot come to mean two things.
func TestAnsweredConflictLoads(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-H2"})
	res := fixture()
	c := alchemy.Conflict{Kind: alchemy.ConflictEntityType, Subject: "e1"}
	c.Left.Provenance.ReviewedBy = "ada"
	res.Conflicts = []alchemy.Conflict{c}
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load of an answered conflict: %v", err)
	}
	if nodeCount(t, l, "run-H2") == 0 {
		t.Fatal("nothing was written for a result whose conflict a person had answered")
	}
}
