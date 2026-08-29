package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/chunk"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// twoSections is one document whose two sections say different things: the
// first states something the ontology declares, the second something it does
// not.
const twoSections = "# Alpha\n\nSuperAI is the cluster in eu-west.\n\n# Beta\n\nA Widget appears here.\n"

func twoSectionsRequest(t *testing.T) Request {
	t.Helper()
	req := regionRequest(t, doc("architecture.md", twoSections))
	// Overlap off. §7.1 has it on by default and for good reason — a relation
	// cut in half by a boundary is recovered by it — but this test is about
	// what happens to one section's records, and an overlapping chunk contains
	// both sections, so with overlap on there is no chunk that is only the
	// rejected one.
	req.Chunking.Overlap = chunk.NoOverlap
	req.Models.LLM = &scriptLLM{replies: map[string]string{
		"eu-west": `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"}}],"relations":[]}`,
		"Widget":  `{"entities":[{"type":"Widget","name":"W1"}],"relations":[]}`,
	}}
	return req
}

// §5c: "Vectors are not reviewable and should not be in the queue... They are
// recomputed for whatever text survives review, which is the only sensible
// ordering: embedding rejected content wastes the call."
//
// The reviewer here throws away everything the second section produced, so the
// second section is text nobody kept, and paying to vectorise it is the waste
// §5c names. The first section is untouched and is embedded.
func TestRejectedContentIsNotWhatGetsEmbedded(t *testing.T) {
	// A reviewer cannot answer a question nobody asked, so the first run is
	// the ask: review mode with nothing decided holds the job and hands back
	// the queue. §5c: "a job under review is held until it is accepted or
	// expires."
	req := twoSectionsRequest(t)
	req.Reviewing = true
	_, err := Run(context.Background(), req, nil)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("Run(review on, nothing decided) = %v, want a *HeldError carrying the queue", err)
	}
	var violation *review.Item
	for i, it := range held.Queue {
		if it.Kind == review.KindViolation {
			violation = &held.Queue[i]
		}
	}
	if violation == nil {
		t.Fatalf("the queue has no violation to reject: %+v", held.Queue)
	}

	emb := &fakeEmbedder{}
	decided := twoSectionsRequest(t)
	decided.Reviewing = true
	decided.Models.Embedder = emb
	decided.Inbox = Answered([]review.Decision{{ItemID: violation.ID, Verb: review.VerbReject, By: "ana"}}, nil)
	res, err := Run(context.Background(), decided, nil)
	if err != nil {
		t.Fatalf("Run(decided): %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].Name != "SuperAI" {
		t.Fatalf("want only the surviving entity, got %+v", res.Entities)
	}
	texts := emb.embedded()
	for _, got := range texts {
		if strings.Contains(got, "Widget") {
			t.Errorf("the rejected section was embedded anyway:\n%q", got)
		}
	}
	if len(texts) != 1 || !strings.Contains(texts[0], "SuperAI") {
		t.Errorf("embedded %d texts %q, want only the section that survived", len(texts), texts)
	}
}

// §3's diagram is the specification, and this is it as an assertion: the
// stages run in that order and the embedder is last. §5c is the reason the
// last two cannot be swapped — "embedding before edits means the vectors
// describe text that has since changed" — and the reason the verifier cannot
// move is §3's own sentence, that an extraction nobody checked is an
// extraction nobody should act on.
func TestTheStagesRunInTheOrderTheDiagramGives(t *testing.T) {
	events := make(chan Event, 256)
	done := make(chan []Event, 1)
	go func() { done <- collect(t, events) }()

	req := twoSectionsRequest(t)
	req.Reviewing = true
	req.Models.Embedder = &fakeEmbedder{}
	req.Inbox = nil
	// Nothing is decided, so the queue holds. Run it once to learn the item
	// ids, then again with them answered so the job reaches the far end.
	held := heldQueue(t, req)
	req.Inbox = Answered(acceptAll(held), nil)

	if _, err := Run(context.Background(), req, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var order []string
	for _, ev := range <-done {
		if ev.Kind == EventStage {
			order = append(order, ev.Stage)
		}
	}
	at := func(stage string) int {
		for i, s := range order {
			if s == stage {
				return i
			}
		}
		return -1
	}
	for _, pair := range [][2]string{
		{stageRead, stageExtract},
		{stageExtract, stageVerify},
		{stageVerify, stageReview},
		{stageReview, stageEmbed},
	} {
		before, after := at(pair[0]), at(pair[1])
		if before < 0 || after < 0 {
			t.Fatalf("stages %v do not include %s and %s", order, pair[0], pair[1])
		}
		if before > after {
			t.Errorf("%s ran after %s: %v", pair[0], pair[1], order)
		}
	}
}

// heldQueue runs a job that is expected to hold and returns its queue.
func heldQueue(t *testing.T, req Request) []review.Item {
	t.Helper()
	_, err := Run(context.Background(), req, nil)
	var held *HeldError
	if !asHeld(err, &held) {
		t.Fatalf("Run = %v, want a hold", err)
	}
	return held.Queue
}

func acceptAll(items []review.Item) []review.Decision {
	out := make([]review.Decision, 0, len(items))
	for _, it := range items {
		out = append(out, review.Decision{ItemID: it.ID, Verb: review.VerbAccept, By: "ana"})
	}
	return out
}

// §5c: "Review adds to provenance; it does not overwrite it" — an accepted
// graph carries who accepted what, and still says a model proposed it.
func TestAReviewedRecordKeepsItsProducerAndGainsItsReviewer(t *testing.T) {
	req := twoSectionsRequest(t)
	req.Reviewing = true
	req.Inbox = Answered(acceptAll(heldQueue(t, req)), nil)

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var reviewed int
	for _, e := range res.Entities {
		if e.Provenance.ReviewedBy == "" {
			continue
		}
		reviewed++
		if e.Provenance.ReviewedBy != "ana" {
			t.Errorf("entity %q reviewed by %q, want ana", e.ID, e.Provenance.ReviewedBy)
		}
		if e.Provenance.Producer != alchemy.ProducerLLMExtract {
			t.Errorf("entity %q lost its producer to the review: %q", e.ID, e.Provenance.Producer)
		}
	}
	if reviewed == 0 {
		t.Error("nothing in the graph records who reviewed it")
	}
}
