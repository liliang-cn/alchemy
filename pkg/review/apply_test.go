package review_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// graph is the fixture most Apply tests decide against: one schema-stated
// entity nobody should be asked about, one model-proposed entity, and one
// model-proposed edge between them.
func graph() alchemy.Result {
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "Cluster", Name: "prod", Provenance: fromSchema},
			{ID: "w1", Type: "Widget", Name: "w", Provenance: fromPDF},
		},
		Relations: []alchemy.Relation{
			{From: "w1", To: "c1", Type: "USES", Provenance: fromPDF},
		},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownEntityType, Subject: "w1",
			Detail: `entity type "Widget" is not declared`, Provenance: fromPDF,
		}},
	}
	res.Counts = alchemy.Counts{
		Entities: 2, Relations: 1, Deterministic: 0, Inferred: 1, Violations: 1,
	}
	return res
}

// A decision naming an item that is not in the queue is a reviewer and a
// service disagreeing about what was asked. Accepting it silently would write
// "reviewed by X" over nothing at all, or over the wrong thing.
func TestADecisionNamingAnItemThatDoesNotExistIsAnError(t *testing.T) {
	res := graph()
	_, _, err := review.Apply(res, nil, []review.Decision{
		{ItemID: "violation/unknown_entity_type/ghost", Verb: review.VerbAccept, By: "ana"},
	})
	if err == nil {
		t.Fatal("err = nil, want an error naming the unknown item")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %q, want it to name the item it could not find", err)
	}
}

// §5c: review adds to provenance. Adding nothing is what applying nothing
// means, and a result that came back marked reviewed after nobody reviewed
// anything is the laundering the whole stage exists to prevent.
func TestApplyingNoDecisionsChangesNothingAndMarksNothingReviewed(t *testing.T) {
	res := graph()
	got, rules, err := review.Apply(res, nil, nil)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(rules) != 0 {
		t.Fatalf("rules = %+v, want none", rules)
	}
	if !reflect.DeepEqual(got, res) {
		t.Fatalf("result changed:\n got %+v\nwant %+v", got, res)
	}
	for _, e := range got.Entities {
		if e.Provenance.ReviewedBy != "" {
			t.Fatalf("entity %q says reviewed by %q; nobody reviewed it", e.ID, e.Provenance.ReviewedBy)
		}
	}
}

// queueOf builds the queue the way a coordinator would, from the same report
// the result was assembled from.
func queueOf(res alchemy.Result, opts review.Options) []review.Item {
	rep := verify.Report{
		Entities:   res.Entities,
		Relations:  res.Relations,
		Violations: res.Violations,
		Conflicts:  res.Conflicts,
	}
	return review.Queue(rep, res, opts)
}

func find(t *testing.T, items []review.Item, id string) review.Item {
	t.Helper()
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %q in %+v", id, items)
	return review.Item{}
}

// §5c: "the provenance of a reviewed edge says llm-extract, reviewed by X
// rather than losing the fact that a model proposed it." Review adds to
// provenance; it does not overwrite it. This is the difference between a graph
// that was checked and a graph that has forgotten it was ever guessed.
func TestAReviewedEdgeStillSaysAModelProposedIt(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true, MinConfidence: 0.9})
	edge := find(t, items, "low_confidence/relation/w1 -[USES]-> c1")

	got, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: edge.ID, Verb: review.VerbAccept, By: "ana"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(got.Relations) != 1 {
		t.Fatalf("relations = %+v, want the accepted edge kept", got.Relations)
	}
	p := got.Relations[0].Provenance
	if p.Producer != alchemy.ProducerLLMExtract {
		t.Fatalf("producer = %q, want %q: review must not overwrite who proposed it", p.Producer, alchemy.ProducerLLMExtract)
	}
	if p.Model != "gemini-3.6-flash-high" || p.Confidence != 0.42 {
		t.Fatalf("provenance = %+v, want the model and its confidence intact", p)
	}
	if p.ReviewedBy != "ana" {
		t.Fatalf("reviewed_by = %q, want %q", p.ReviewedBy, "ana")
	}
}

// A rejected item leaves the graph. Marking it reviewed and keeping it would
// mean a reviewer's "no" produced a record that says a person approved it.
func TestARejectedItemIsRemovedFromTheGraph(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true})
	v := find(t, items, "violation/unknown_entity_type/w1")

	got, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: v.ID, Verb: review.VerbReject, By: "ana", Note: "not a thing"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	for _, e := range got.Entities {
		if e.ID == "w1" {
			t.Fatalf("entities = %+v, want w1 gone", got.Entities)
		}
	}
	// The edge went with it. A reviewer who says an entity is not real did not
	// ask for a dangling-relation violation nobody's source produced.
	if len(got.Relations) != 0 {
		t.Fatalf("relations = %+v, want the edge that named w1 removed with it", got.Relations)
	}
	if got.Counts.Entities != 1 || got.Counts.Relations != 0 {
		t.Fatalf("counts = %+v, want them recomputed from what survived", got.Counts)
	}
	// The violation is still reported, and now says who answered it. A finding
	// that disappeared when somebody decided it would leave a result
	// indistinguishable from one that never had a problem.
	if len(got.Violations) != 1 || got.Violations[0].Provenance.ReviewedBy != "ana" {
		t.Fatalf("violations = %+v, want the finding kept and stamped", got.Violations)
	}
}

// An edited item carries both the edit and the fact a model proposed the
// original.
func TestAnEditedItemKeepsTheModelThatProposedTheOriginal(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true})
	v := find(t, items, "violation/unknown_entity_type/w1")

	got, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: v.ID, Verb: review.VerbEdit, By: "ana", Edit: &review.Edit{Type: "Node", Name: "node-w"}},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	var w alchemy.Entity
	for _, e := range got.Entities {
		if e.ID == "w1" {
			w = e
		}
	}
	if w.Type != "Node" || w.Name != "node-w" {
		t.Fatalf("entity = %+v, want it retyped and renamed", w)
	}
	if w.Provenance.Producer != alchemy.ProducerLLMExtract || w.Provenance.ReviewedBy != "ana" {
		t.Fatalf("provenance = %+v, want llm-extract reviewed by ana", w.Provenance)
	}
}

// An edit that changes nothing is refused. A reviewer who pressed Edit and got
// back a record marked reviewed and otherwise untouched has been told their
// correction landed when it did not.
func TestAnEditThatChangesNothingIsRefused(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true})
	v := find(t, items, "violation/unknown_entity_type/w1")

	if _, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: v.ID, Verb: review.VerbEdit, By: "ana", Edit: &review.Edit{}},
	}); err == nil {
		t.Fatal("err = nil, want an empty edit refused")
	}
}

// A decision nobody signed cannot be written into provenance.
func TestADecisionWithNoReviewerIsRefused(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true})
	v := find(t, items, "violation/unknown_entity_type/w1")

	if _, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: v.ID, Verb: review.VerbAccept},
	}); err == nil {
		t.Fatal("err = nil, want a decision with no reviewer refused")
	}
}

// Decisions are a set, not a script. Two people working two halves of one
// queue must not produce two different graphs depending on whose answers
// arrived first.
func TestTheOrderOfDecisionsDoesNotChangeTheOutcome(t *testing.T) {
	res := widgets()
	res.Relations = []alchemy.Relation{{From: "w1", To: "w2", Type: "USES", Provenance: fromPDF}}
	res.Counts.Relations = 1
	res.Counts.Inferred = 1
	items := queueOf(res, review.Options{Reviewing: true, MinConfidence: 0.9})

	ds := []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbReject, By: "ana"},
		{ItemID: "violation/unknown_entity_type/w2", Verb: review.VerbEdit, By: "bo", Edit: &review.Edit{Type: "Node"}},
		{ItemID: "violation/unknown_entity_type/g1", Verb: review.VerbAlways, By: "ana", Note: "fine"},
	}
	forward, rulesA, err := review.Apply(res, items, ds)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	reversed := []review.Decision{ds[2], ds[1], ds[0]}
	backward, rulesB, err := review.Apply(res, items, reversed)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !reflect.DeepEqual(forward, backward) {
		t.Fatalf("order changed the result:\n%+v\n%+v", forward, backward)
	}
	if !reflect.DeepEqual(rulesA, rulesB) {
		t.Fatalf("order changed the rules:\n%+v\n%+v", rulesA, rulesB)
	}
}

// Two different answers to one question is a contradiction, and this package's
// whole subject is that contradictions get asked rather than silently settled.
// "Last wins" would need an order, which is the thing the test above says does
// not exist.
func TestTwoDifferentDecisionsOnOneItemAreAnErrorRatherThanTheLastOneWinning(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true})
	_, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAccept, By: "ana"},
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbReject, By: "bo"},
	})
	if err == nil {
		t.Fatal("err = nil, want two different decisions on one item refused")
	}
	if !strings.Contains(err.Error(), "w1") {
		t.Fatalf("err = %q, want it to name the item", err)
	}
}

// A stream that reconnects redelivers (§6: Review is bidirectional). The same
// answer arriving twice is not two answers.
func TestTheSameDecisionDeliveredTwiceIsNotAContradiction(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true})
	d := review.Decision{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAccept, By: "ana"}
	if _, _, err := review.Apply(res, items, []review.Decision{d, d}); err != nil {
		t.Fatalf("err = %v, want a redelivered decision accepted", err)
	}
}

// A guess is a mapping, not a record: the rows it produced carry no
// back-reference to it. Telling a reviewer their retype landed when nothing in
// the graph moved is worse than telling them it cannot.
func TestAGuessCanBeAcceptedButNotRejectedOrEdited(t *testing.T) {
	res := graph()
	res.Guesses = []alchemy.Guess{{Field: "cust_id", ChosenAs: "customer.id", Provenance: fromTable}}
	res.Counts.Guesses = 1
	items := queueOf(res, review.Options{Reviewing: true})
	id := find(t, items, "guess/customers.csv:cust_id").ID

	for _, verb := range []review.Verb{review.VerbReject, review.VerbEdit} {
		d := review.Decision{ItemID: id, Verb: verb, By: "ana", Edit: &review.Edit{Type: "Node"}}
		if _, _, err := review.Apply(res, items, []review.Decision{d}); err == nil {
			t.Fatalf("verb %q on a mapping: err = nil, want it refused", verb)
		}
	}
	got, _, err := review.Apply(res, items, []review.Decision{{ItemID: id, Verb: review.VerbAccept, By: "ana"}})
	if err != nil {
		t.Fatalf("err = %v, want an accepted mapping to work", err)
	}
	if got.Guesses[0].Provenance.ReviewedBy != "ana" {
		t.Fatalf("guess = %+v, want it stamped with who approved the mapping", got.Guesses[0])
	}
}

// §5c: review adds to provenance. A second reviewer does not erase the first.
func TestASecondRoundOfReviewAddsAReviewerRatherThanReplacingOne(t *testing.T) {
	res := graph()
	items := queueOf(res, review.Options{Reviewing: true})
	first, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAccept, By: "ana"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	second, _, err := review.Apply(first, queueOf(first, review.Options{Reviewing: true}), []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAccept, By: "bo"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	for _, e := range second.Entities {
		if e.ID == "w1" && e.Provenance.ReviewedBy != "ana, bo" {
			t.Fatalf("reviewed_by = %q, want both names", e.Provenance.ReviewedBy)
		}
	}
}
