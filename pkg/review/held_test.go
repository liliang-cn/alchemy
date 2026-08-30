package review_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

func conflicted() alchemy.Result {
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "n1", Type: "Node", Name: "node-1", Provenance: fromSchema},
			{ID: "n1", Type: "StoragePool", Name: "node-1", Provenance: fromPDF},
		},
		Conflicts: []alchemy.Conflict{{
			Kind: alchemy.ConflictEntityType, Subject: "n1",
			Detail: `entity "n1" is typed "Node" by schema.sql and "StoragePool" by contract.pdf`,
			Left:   alchemy.Claim{Statement: `entity "n1" is of type "Node"`, Provenance: fromSchema},
			Right:  alchemy.Claim{Statement: `entity "n1" is of type "StoragePool"`, Provenance: fromPDF},
		}},
	}
	res.Counts = alchemy.Counts{Entities: 2, Conflicts: 1}
	return res
}

// §7.3: a job that finds a conflict does not finish. It reaches NEEDS_REVIEW
// and stays there until someone resolves it, whether or not the caller asked
// for review — so a coordinator needs to be able to ask a result whether the
// question has been answered yet, and to get the question back if it has not.
func TestAJobIsHeldUntilEveryConflictHasBeenAnsweredByAPerson(t *testing.T) {
	res := conflicted()
	if open := res.Held(); len(open) != 1 {
		t.Fatalf("held = %+v, want the unanswered conflict", open)
	}

	items := queueOf(res, review.Options{})
	got, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbReject, By: "ana", Note: "the schema is right"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if open := got.Held(); len(open) != 0 {
		t.Fatalf("held = %+v, want nothing: a person answered it", open)
	}
	// The conflict is still reported. §5b: a graph reports its own quality,
	// and a job that was held, asked and answered does not get to look like
	// one that never had a question.
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want the finding kept", got.Conflicts)
	}
}

// §7.3: the operator can "tell the service how to resolve conflicts of that
// shape next time, which is how a pipeline that started attended becomes one
// that runs itself without ever having guessed." That only works if a rule
// answers the conflict rather than hiding it — a rule that merely kept the
// item out of the queue would leave tonight's job held on a question nobody is
// being shown.
func TestARuleAnswersTonightsConflictRatherThanHidingIt(t *testing.T) {
	yesterday := conflicted()
	_, rules, err := review.Apply(yesterday, queueOf(yesterday, review.Options{}), []review.Decision{
		{ItemID: "conflict/entity_type/n1", Verb: review.VerbAlways, By: "ana",
			Edit: &review.Edit{Type: "Node"}, Note: "the schema wins on type"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}

	tonight := conflicted()
	items := queueOf(tonight, review.Options{Rules: rules})
	if len(review.Open(items)) != 0 {
		t.Fatalf("open = %+v, want nobody asked again", review.Open(items))
	}

	got, _, err := review.Apply(tonight, items, nil)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if open := got.Held(); len(open) != 0 {
		t.Fatalf("held = %+v, want the rule to have answered it", open)
	}
	// And the answer is the one the rule recorded, carrying the name of the
	// person who recorded it — not "the rule" and not nobody.
	for _, e := range got.Entities {
		if e.Provenance.Source == "contract.pdf" {
			if e.Type != "Node" {
				t.Fatalf("entity = %+v, want the rule's edit applied", e)
			}
			if e.Provenance.ReviewedBy != "ana" {
				t.Fatalf("reviewed_by = %q, want the person whose rule it is", e.Provenance.ReviewedBy)
			}
		}
	}
}
