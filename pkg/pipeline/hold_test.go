package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/chunk"
)

// §7.3's table, left column. "A violation is one source saying something the
// ontology does not allow: attributable, excludable, and the rest of the graph
// is usable without it." So the job finishes, the graph is delivered, and the
// violation is in it — not silently dropped and not silently kept.
func TestViolationsDoNotHoldTheJob(t *testing.T) {
	req := twoSectionsRequest(t)
	req.Chunking.Overlap = chunk.NoOverlap
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v; a violation does not hold a job", err)
	}
	if len(res.Violations) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(res.Violations), res.Violations)
	}
	if got := res.Violations[0].Kind; got != alchemy.ViolationUnknownEntityType {
		t.Errorf("kind = %q, want %q", got, alchemy.ViolationUnknownEntityType)
	}
	// The rest of the graph is usable without it, and the offending record is
	// still there to be excluded by whoever reads the violation.
	if len(res.Entities) != 2 {
		t.Errorf("want both entities delivered, got %d: %+v", len(res.Entities), res.Entities)
	}
}

// §7.3's table, bottom row, in both columns: "Conflicts | job holds — a person
// must decide | queued". Asking for review changes what else is in the queue;
// it does not change this.
func TestAConflictHoldsTheJobWhetherOrNotReviewWasAskedFor(t *testing.T) {
	for _, reviewing := range []bool{false, true} {
		req := regionRequest(t, doc("eu.md", docEU), doc("us.md", docUS))
		req.Reviewing = reviewing
		res, err := Run(context.Background(), req, nil)

		var held *HeldError
		if !errors.As(err, &held) {
			t.Fatalf("Run(reviewing=%v) = %v, want a *HeldError", reviewing, err)
		}
		// The graph is not in the first return value at all. A caller who
		// ignored the error would get nothing to act on, which is the point:
		// a held job must not be mistakable for a finished one.
		if len(res.Entities) != 0 || len(res.Relations) != 0 || len(res.Chunks) != 0 {
			t.Errorf("reviewing=%v: Run returned a graph alongside the hold: %+v", reviewing, res)
		}
		if len(held.Pending.Entities) == 0 {
			t.Errorf("reviewing=%v: the held graph is empty; it should be reachable by naming the hold", reviewing)
		}
		if len(held.Queue) == 0 {
			t.Errorf("reviewing=%v: a held job asks nobody anything", reviewing)
		}
		if held.State() != alchemy.JobNeedsReview {
			t.Errorf("state = %q, want %q", held.State(), alchemy.JobNeedsReview)
		}
		// §5c: vectors are not spent on a job that has not survived review.
		if len(held.Pending.Vectors) != 0 {
			t.Errorf("reviewing=%v: a held job bought %d vectors", reviewing, len(held.Pending.Vectors))
		}
	}
}
