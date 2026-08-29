package review_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// An Item names a finding by position, so Apply must be given the result the
// queue was built from. A range check catches the coordinator who hands over a
// shorter result; it does not catch the one who hands over a result of the
// right length whose findings moved — and that one is worse, because it stamps
// a reviewer's name on a finding they never saw. §5c's whole claim is "the
// model proposed, and what you have was checked", and a name attached to the
// wrong finding is that claim being false in the most quiet way available.
//
// pkg/pipeline found this the hard way: Queue reads conflicts and violations
// out of verify.Report while Apply reads them out of alchemy.Result, and
// keeping the two aligned was an invariant nothing checked.
func TestApplyRefusesAResultWhoseFindingsMoved(t *testing.T) {
	res := alchemy.Result{
		Violations: []alchemy.Violation{
			{Kind: alchemy.ViolationUnknownEntityType, Subject: "n1", Detail: "first",
				Provenance: alchemy.Provenance{Source: "a.md", Producer: alchemy.ProducerLLMExtract}},
			{Kind: alchemy.ViolationUnknownRelationType, Subject: "n1 -[T]-> n2", Detail: "second",
				Provenance: alchemy.Provenance{Source: "a.md", Producer: alchemy.ProducerLLMExtract}},
		},
	}
	items := review.Queue(verify.Report{Violations: res.Violations}, res, review.Options{Reviewing: true})
	if len(items) != 2 {
		t.Fatalf("queue = %d items, want 2", len(items))
	}

	// A coordinator appends a reader's own violation in front of the
	// verifier's. Same length is not enough — nothing moved out of range, the
	// findings simply are not the ones the queue described.
	moved := res
	moved.Violations = []alchemy.Violation{
		{Kind: alchemy.ViolationMalformedRow, Subject: "orders.csv line 12", Detail: "a row nobody reviewed",
			Provenance: alchemy.Provenance{Source: "orders.csv", Producer: alchemy.ProducerTabular}},
		res.Violations[0],
	}

	out, _, err := review.Apply(moved, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbAccept, By: "ana"},
	})
	if err == nil {
		t.Fatalf("Apply accepted a result whose findings moved; violations came back as %+v", out.Violations)
	}
	if !strings.Contains(err.Error(), items[0].ID) {
		t.Errorf("error = %q, want it to name the item that did not match", err)
	}
}

// The ordinary case must stay ordinary: the same result the queue was built
// from is accepted and stamped.
func TestApplyAcceptsTheResultTheQueueWasBuiltFrom(t *testing.T) {
	res := alchemy.Result{
		Violations: []alchemy.Violation{
			{Kind: alchemy.ViolationUnknownEntityType, Subject: "n1", Detail: "first",
				Provenance: alchemy.Provenance{Source: "a.md", Producer: alchemy.ProducerLLMExtract}},
		},
	}
	items := review.Queue(verify.Report{Violations: res.Violations}, res, review.Options{Reviewing: true})
	out, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbAccept, By: "ana"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out.Violations[0].Provenance.ReviewedBy != "ana" {
		t.Errorf("ReviewedBy = %q, want ana", out.Violations[0].Provenance.ReviewedBy)
	}
	if out.Violations[0].Provenance.Producer != alchemy.ProducerLLMExtract {
		t.Errorf("producer = %q; review adds to provenance, it does not overwrite it", out.Violations[0].Provenance.Producer)
	}
}
