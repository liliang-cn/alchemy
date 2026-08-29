package review_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// widgets builds a result with three undeclared entity types from the same
// model: two Widgets and one Gadget. The two Widgets are the same kind of
// mistake; the Gadget is a different one.
func widgets() alchemy.Result {
	pdf2 := fromPDF
	pdf2.Chunk = 77
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "w1", Type: "Widget", Name: "left", Provenance: fromPDF},
			{ID: "w2", Type: "Widget", Name: "right", Provenance: pdf2},
			{ID: "g1", Type: "Gadget", Name: "other", Provenance: pdf2},
		},
		Violations: []alchemy.Violation{
			{Kind: alchemy.ViolationUnknownEntityType, Subject: "w1", Detail: `"Widget" is not declared`, Provenance: fromPDF},
			{Kind: alchemy.ViolationUnknownEntityType, Subject: "w2", Detail: `"Widget" is not declared`, Provenance: pdf2},
			{Kind: alchemy.ViolationUnknownEntityType, Subject: "g1", Detail: `"Gadget" is not declared`, Provenance: pdf2},
		},
	}
	res.Counts = alchemy.Counts{Entities: 3, Violations: 3}
	return res
}

// §5c: "Reviewing a thousand extractions one at a time is not a workflow
// anybody sustains; reviewing the twelve kinds of mistake in them is." A rule
// made about one Widget covers the other Widget — different entity, different
// chunk, same mistake.
func TestAnAlwaysRuleSuppressesTheSameMistakeAboutADifferentRecord(t *testing.T) {
	res := widgets()
	items := queueOf(res, review.Options{Reviewing: true})

	_, rules, err := review.Apply(res, items, []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAlways, By: "ana", Note: "widen the ontology next release"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want exactly one", rules)
	}

	next := review.Open(queueOf(res, review.Options{Reviewing: true, Rules: rules}))
	for _, it := range next {
		if strings.Contains(it.Subject, "w") {
			t.Fatalf("queue = %+v, want both Widgets suppressed by the rule", next)
		}
	}
	// And the Gadget is still asked about. A rule that covered it would be
	// accepting a type nobody ever looked at.
	if len(next) != 1 || next[0].Subject != "g1" {
		t.Fatalf("queue = %+v, want the Gadget still asked about", next)
	}
}

// §5c: "A rule is recorded with the decision that produced it, so a later
// reader can see why the rule exists." A rule with no origin is a policy
// nobody can explain, and the queue quietly shrinking for reasons nobody can
// audit is the failure this package exists to prevent.
func TestARuleCarriesTheDecisionThatProducedIt(t *testing.T) {
	res := widgets()
	items := queueOf(res, review.Options{Reviewing: true})
	d := review.Decision{
		ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAlways,
		By: "ana", Note: "widen the ontology next release",
	}

	_, rules, err := review.Apply(res, items, []review.Decision{d})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	r := rules[0]
	if r.From.By != "ana" || r.From.Note != d.Note || r.From.Verb != review.VerbAlways {
		t.Fatalf("rule.From = %+v, want the whole decision", r.From)
	}
	if r.From.ItemID != d.ItemID {
		t.Fatalf("rule.From.ItemID = %q, want the item it was made on", r.From.ItemID)
	}
	if !strings.Contains(r.Because, "Widget") {
		t.Fatalf("rule.Because = %q, want the sentence the reviewer was reading", r.Because)
	}
	if r.Kind != review.KindViolation {
		t.Fatalf("rule.Kind = %q, want %q", r.Kind, review.KindViolation)
	}
}

// A rule made about one producer does not cover another. The producer is why
// the reviewer believed the item: "the schema may say Widget" is not "the
// model may say Widget", and a rule that crossed that line would accept a
// model's proposal on the strength of a decision made about a CREATE TABLE.
func TestARuleDoesNotCrossFromOneProducerToAnother(t *testing.T) {
	res := widgets()
	rules := []review.Rule{{
		Shape: "violation/unknown_entity_type/type=Widget/producer=ddl",
		Kind:  review.KindViolation,
	}}

	next := review.Open(queueOf(res, review.Options{Reviewing: true, Rules: rules}))
	if len(next) != 3 {
		t.Fatalf("queue = %+v, want all three still asked about", next)
	}
}

// Covers is exported so a queue can say why an item is not in it. §5c's
// argument for recording the decision behind a rule is that a policy nobody
// can explain is the failure; a suppression nobody can attribute is the same
// failure one step earlier.
func TestARuleCanSayWhichItemItCovers(t *testing.T) {
	res := widgets()
	items := queueOf(res, review.Options{Reviewing: true})
	_, rules, err := review.Apply(res, items, []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAlways, By: "ana"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	var covered, missed int
	for _, it := range items {
		if rules[0].Covers(it) {
			covered++
		} else {
			missed++
		}
	}
	if covered != 2 || missed != 1 {
		t.Fatalf("covered = %d, missed = %d, want the two Widgets and not the Gadget", covered, missed)
	}
}

// §7.3: the operator's options are "resolve it, or tell the service how to
// resolve conflicts of that shape next time — which is how a pipeline that
// started attended becomes one that runs itself without ever having guessed."
// The shape of a conflict is the pair of producers that disagreed and what
// they disagreed about, not which entity they disagreed about.
func TestAConflictRuleCoversTheSameDisagreementAboutADifferentEntity(t *testing.T) {
	pdf2 := fromPDF
	pdf2.Chunk = 88
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "n1", Type: "Node", Provenance: fromSchema},
			{ID: "n1", Type: "StoragePool", Provenance: fromPDF},
			{ID: "n2", Type: "Node", Provenance: fromSchema},
			{ID: "n2", Type: "StoragePool", Provenance: pdf2},
		},
		Conflicts: []alchemy.Conflict{
			{
				Kind: alchemy.ConflictEntityType, Subject: "n1", Detail: "n1 is typed twice",
				Left:  alchemy.Claim{Statement: "n1 is Node", Provenance: fromSchema},
				Right: alchemy.Claim{Statement: "n1 is StoragePool", Provenance: fromPDF},
			},
			{
				Kind: alchemy.ConflictEntityType, Subject: "n2", Detail: "n2 is typed twice",
				Left:  alchemy.Claim{Statement: "n2 is Node", Provenance: fromSchema},
				Right: alchemy.Claim{Statement: "n2 is StoragePool", Provenance: pdf2},
			},
		},
	}
	res.Counts = alchemy.Counts{Entities: 4, Conflicts: 2}
	items := queueOf(res, review.Options{})

	// "The schema wins over the model on entity type." One answer, stated once.
	_, rules, err := review.Apply(res, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbAlways, By: "ana",
			Edit: &review.Edit{Type: "Node"}, Note: "the schema wins on type"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}

	next := review.Open(queueOf(res, review.Options{Rules: rules}))
	if len(next) != 0 {
		t.Fatalf("queue = %+v, want both disagreements of this shape settled", next)
	}
	// A relation-direction disagreement between the same two producers is a
	// different question and is still asked.
	other := review.Item{Kind: review.KindConflict, Shape: "conflict/relation_direction/between=ddl|llm-extract/model=gemini-3.6-flash-high"}
	if rules[0].Covers(other) {
		t.Fatalf("rule %q covers a different kind of disagreement", rules[0].Shape)
	}
}

// An `always` about a conflict is still an answer to the conflict in front of
// the reviewer: §7.3's job stays held until someone decides, and a rule that
// silenced future questions without answering the present one would leave the
// job blocked on an item the queue no longer shows.
func TestAnAlwaysOnAConflictAnswersTheConflictItWasMadeOn(t *testing.T) {
	res := conflicted()
	items := queueOf(res, review.Options{})
	got, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbAlways, By: "ana", Note: "the schema wins"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if open := review.Held(got); len(open) != 0 {
		t.Fatalf("held = %+v, want the conflict answered", open)
	}
}

// A person beats a policy. Somebody who looked at a suppressed item anyway
// has overruled the rule for that item, and their answer is the one that
// lands — otherwise a rule would be irreversible without editing the rule
// list, which is a worse workflow than the one it replaced.
func TestAnExplicitDecisionOverrulesTheRuleThatWouldHaveAnsweredIt(t *testing.T) {
	res := widgets()
	items := queueOf(res, review.Options{Reviewing: true, Rules: []review.Rule{{
		Shape: "violation/unknown_entity_type/type=Widget/producer=llm-extract/model=gemini-3.6-flash-high",
		Kind:  review.KindViolation,
		From:  review.Decision{Verb: review.VerbAlways, By: "ana", Note: "widgets are fine"},
	}}})

	got, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbReject, By: "bo", Note: "not this one"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	var ids []string
	for _, e := range got.Entities {
		ids = append(ids, e.ID)
	}
	if len(ids) != 2 || ids[0] != "w2" {
		t.Fatalf("entities = %v, want w1 rejected and w2 kept by the rule", ids)
	}
	for _, e := range got.Entities {
		if e.ID == "w2" && e.Provenance.ReviewedBy != "ana" {
			t.Fatalf("w2 reviewed_by = %q, want the rule's author", e.Provenance.ReviewedBy)
		}
	}
}
