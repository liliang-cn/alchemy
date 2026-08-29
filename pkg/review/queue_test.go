package review_test

import (
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

var (
	fromSchema = alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	fromPDF    = alchemy.Provenance{
		Source: "contract.pdf", Chunk: 41, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Confidence: 0.42,
	}
	fromTable = alchemy.Provenance{Source: "customers.csv", Chunk: -1, Producer: alchemy.ProducerTabular}
)

// §5c states the reason as well as the rule: "Asking a person to confirm what
// the schema states is how you teach them to click Approve without reading."
// A queue that includes the obvious is a queue people stop reading, and then
// the review is worse than none — it launders unchecked output as reviewed.
func TestAskingAPersonToConfirmWhatTheSchemaStatesTeachesThemToClickApprove(t *testing.T) {
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "n1", Type: "Node", Name: "node-1", Provenance: fromSchema},
			{ID: "c1", Type: "Cluster", Name: "prod", Provenance: fromSchema},
		},
		Relations: []alchemy.Relation{
			{From: "n1", To: "c1", Type: "MEMBER_OF", Provenance: fromSchema},
		},
	}
	rep := verify.Report{Entities: res.Entities, Relations: res.Relations}

	got := review.Queue(rep, res, review.Options{Reviewing: true})

	if len(got) != 0 {
		t.Fatalf("queue = %+v, want nothing: a CREATE TABLE said all of it", got)
	}
}

// The order is §5c's table, and it is not a preference: a conflict is a
// question nothing in the data can answer, a violation is one source breaking
// a rule, a guess misaligns a whole table, and an unsure edge is one edge.
func TestTheQueueIsRankedConflictsViolationsGuessesThenLowConfidence(t *testing.T) {
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "n1", Type: "Node", Name: "node-1", Provenance: fromSchema},
			{ID: "n1", Type: "StoragePool", Name: "node-1", Provenance: fromPDF},
			{ID: "w1", Type: "Widget", Name: "w", Provenance: fromPDF},
		},
		Relations: []alchemy.Relation{
			{From: "n1", To: "w1", Type: "USES", Provenance: fromPDF},
		},
		Guesses: []alchemy.Guess{
			{Field: "cust_id", ChosenAs: "customer.id", Alternatives: []string{"customer.ref"}, Provenance: fromTable},
		},
	}
	rep := verify.Report{
		Entities:  res.Entities,
		Relations: res.Relations,
		Conflicts: []alchemy.Conflict{{
			Kind: alchemy.ConflictEntityType, Subject: "n1", Detail: "two types",
			Left:  alchemy.Claim{Statement: "n1 is Node", Provenance: fromSchema},
			Right: alchemy.Claim{Statement: "n1 is StoragePool", Provenance: fromPDF},
		}},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownEntityType, Subject: "w1",
			Detail: "entity type \"Widget\" is not declared", Provenance: fromPDF,
		}},
	}

	got := review.Queue(rep, res, review.Options{Reviewing: true, MinConfidence: 0.8})

	var kinds []review.Kind
	for i, it := range got {
		if it.Rank != i {
			t.Fatalf("item %d has rank %d; rank must be the position in the queue", i, it.Rank)
		}
		kinds = append(kinds, it.Kind)
	}
	want := []review.Kind{review.KindConflict, review.KindViolation, review.KindGuess, review.KindLowConfidence}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
}

// §7.3: a job can reach NEEDS_REVIEW without review mode being on, because a
// conflict always requires a person. The queue is how that job gets unblocked,
// so it has to work for a caller who never asked for one — and it must not
// take the opportunity to ask them about everything else.
func TestConflictsAreQueuedForACallerWhoNeverAskedForReview(t *testing.T) {
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "n1", Type: "Node", Name: "node-1", Provenance: fromSchema},
			{ID: "n1", Type: "StoragePool", Name: "node-1", Provenance: fromPDF},
			{ID: "w1", Type: "Widget", Name: "w", Provenance: fromPDF},
		},
		Guesses: []alchemy.Guess{{Field: "cust_id", ChosenAs: "customer.id", Provenance: fromTable}},
	}
	rep := verify.Report{
		Entities: res.Entities,
		Conflicts: []alchemy.Conflict{{
			Kind: alchemy.ConflictEntityType, Subject: "n1", Detail: "two types",
			Left:  alchemy.Claim{Statement: "n1 is Node", Provenance: fromSchema},
			Right: alchemy.Claim{Statement: "n1 is StoragePool", Provenance: fromPDF},
		}},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownEntityType, Subject: "w1",
			Detail: "Widget is not declared", Provenance: fromPDF,
		}},
	}

	// The zero Options is a caller that never turned review on.
	got := review.Queue(rep, res, review.Options{})

	if len(got) != 1 {
		t.Fatalf("queue = %+v, want the conflict and nothing else", got)
	}
	if got[0].Kind != review.KindConflict {
		t.Fatalf("kind = %q, want %q", got[0].Kind, review.KindConflict)
	}
	// §7.3's table: with review off a violation is returned and the graph is
	// delivered. Queueing it would make review compulsory by the back door.
	if got[0].Subject != "n1" {
		t.Fatalf("subject = %q, want the conflicted entity", got[0].Subject)
	}
}

// A conflict item points at the dissenting record only. Verify puts the
// incumbent — and, for a contradiction, the deterministic side — on the left,
// so a rejection here removes the newcomer and leaves the schema's record
// standing rather than deleting both sides of the question.
func TestAConflictItemTargetsTheDissentingRecordNotBothSides(t *testing.T) {
	res := alchemy.Result{Entities: []alchemy.Entity{
		{ID: "n1", Type: "Node", Name: "node-1", Provenance: fromSchema},
		{ID: "n1", Type: "StoragePool", Name: "node-1", Provenance: fromPDF},
	}}
	rep := verify.Report{
		Entities: res.Entities,
		Conflicts: []alchemy.Conflict{{
			Kind: alchemy.ConflictEntityType, Subject: "n1", Detail: "two types",
			Left:  alchemy.Claim{Statement: "n1 is Node", Provenance: fromSchema},
			Right: alchemy.Claim{Statement: "n1 is StoragePool", Provenance: fromPDF},
		}},
	}

	got := review.Queue(rep, res, review.Options{})

	if len(got) != 1 || len(got[0].Targets) != 1 {
		t.Fatalf("targets = %+v, want exactly the dissenting record", got)
	}
	tgt := got[0].Targets[0]
	if tgt.ID != "n1" || tgt.Type != "StoragePool" || tgt.Provenance.Source != "contract.pdf" {
		t.Fatalf("target = %+v, want the PDF's StoragePool record", tgt)
	}
}

// A record that is already the subject of a higher-ranked item is not asked
// about twice. Two entries for one edge is one person answering one question
// twice, and the second answer is the one they stop reading.
func TestARecordAlreadyQueuedForAViolationIsNotQueuedAgainForLowConfidence(t *testing.T) {
	res := alchemy.Result{Entities: []alchemy.Entity{{ID: "w1", Type: "Widget", Provenance: fromPDF}}}
	rep := verify.Report{
		Entities: res.Entities,
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownEntityType, Subject: "w1",
			Detail: "Widget is not declared", Provenance: fromPDF,
		}},
	}

	got := review.Queue(rep, res, review.Options{Reviewing: true, MinConfidence: 0.9})

	if len(got) != 1 || got[0].Kind != review.KindViolation {
		t.Fatalf("queue = %+v, want one violation and no second item about w1", got)
	}
}

// §5c's row is "the model was unsure and said so". A producer that reports no
// confidence at all has said nothing, and reading its silence as doubt would
// put every record in the queue.
func TestAProducerThatReportsNoConfidenceIsNotTreatedAsUnsure(t *testing.T) {
	silent := alchemy.Provenance{Source: "notes.md", Chunk: 2, Producer: alchemy.ProducerLLMExtract, Model: "m"}
	res := alchemy.Result{Entities: []alchemy.Entity{{ID: "e1", Type: "Node", Provenance: silent}}}
	rep := verify.Report{Entities: res.Entities}

	if got := review.Queue(rep, res, review.Options{Reviewing: true, MinConfidence: 0.9}); len(got) != 0 {
		t.Fatalf("queue = %+v, want nothing", got)
	}
}

// The stronger form of §5c's last row, over a mixed input: a record is never
// queued for what it is. A deterministic producer reports no confidence, so
// nothing it states can ever be the model saying it was unsure — and if it
// could, this is the row that says do not ask anyway.
//
// A conflict or a violation involving a schema is a different matter and is
// queued: "your schema and your ontology disagree" and "your schema and your
// PDF disagree" are questions about two things, not requests to confirm what
// one of them says. §7.3 is explicit that a conflict holds the job no matter
// which producers made it.
func TestNoRecordIsEverQueuedMerelyForBeingARecord(t *testing.T) {
	det := alchemy.Provenance{Source: "graph.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Confidence: 0.1}
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "a", Type: "Node", Provenance: fromSchema},
			{ID: "b", Type: "Node", Provenance: det},
			{ID: "c", Type: "Node", Provenance: fromPDF},
		},
		Relations: []alchemy.Relation{
			{From: "a", To: "b", Type: "MEMBER_OF", Provenance: fromSchema},
			{From: "b", To: "c", Type: "MEMBER_OF", Provenance: det},
		},
		Guesses: []alchemy.Guess{{Field: "id", ChosenAs: "node.id", Provenance: fromSchema}},
	}
	rep := verify.Report{Entities: res.Entities, Relations: res.Relations}

	for _, opts := range []review.Options{
		{},
		{Reviewing: true},
		{Reviewing: true, MinConfidence: 1.0},
	} {
		for _, it := range review.Queue(rep, res, opts) {
			if it.Kind != review.KindLowConfidence {
				continue
			}
			if it.Provenance.Producer.Deterministic() {
				t.Fatalf("opts %+v queued %q: a CREATE TABLE said it", opts, it.ID)
			}
			for _, tgt := range it.Targets {
				if tgt.Provenance.Producer.Deterministic() {
					t.Fatalf("opts %+v queued a deterministic record via %q", opts, it.ID)
				}
			}
		}
	}
}

// A rule about one model's low-confidence PART_OF edges is a claim an operator
// can hold. It must not become a claim about a different model, which is a
// different extractor with a different failure mode wearing the same rule.
func TestALowConfidenceRuleIsPinnedToOneTypeAndOneModel(t *testing.T) {
	other := fromPDF
	other.Model = "some-other-model"
	elsewhere := fromPDF
	elsewhere.Chunk = 99

	res := alchemy.Result{Relations: []alchemy.Relation{
		{From: "a", To: "b", Type: "PART_OF", Provenance: fromPDF},
		{From: "c", To: "d", Type: "PART_OF", Provenance: elsewhere},
		{From: "e", To: "f", Type: "PART_OF", Provenance: other},
		{From: "g", To: "h", Type: "USES", Provenance: fromPDF},
	}}
	rep := verify.Report{Relations: res.Relations}
	opts := review.Options{Reviewing: true, MinConfidence: 0.9}

	items := review.Queue(rep, res, opts)
	if len(items) != 4 {
		t.Fatalf("queue = %+v, want all four", items)
	}
	_, rules, err := review.Apply(res, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbAlways, By: "ana", Note: "this model is fine about PART_OF"},
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}

	opts.Rules = rules
	next := review.Open(review.Queue(rep, res, opts))
	if len(next) != 2 {
		t.Fatalf("queue = %+v, want the other model's PART_OF and this model's USES still asked about", next)
	}
	for _, it := range next {
		if it.Provenance.Model == "gemini-3.6-flash-high" && it.Subject == "c -[PART_OF]-> d" {
			t.Fatalf("item %q survived a rule that covers it", it.ID)
		}
	}
}

// Two findings can name one subject. Their items must still be tellable apart,
// or a decision on one is a decision on whichever the map happened to hold.
func TestTwoFindingsAboutOneSubjectGetDistinctItemIDs(t *testing.T) {
	res := alchemy.Result{Entities: []alchemy.Entity{{ID: "n1", Type: "Widget", Provenance: fromPDF}}}
	rep := verify.Report{
		Entities: res.Entities,
		Violations: []alchemy.Violation{
			{Kind: alchemy.ViolationUnknownEntityType, Subject: "n1", Detail: "one", Provenance: fromPDF},
			{Kind: alchemy.ViolationUnknownEntityType, Subject: "n1", Detail: "two", Provenance: fromPDF},
		},
	}

	got := review.Queue(rep, res, review.Options{Reviewing: true})
	if len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("ids = %+v, want two distinct items", got)
	}
}

// The queue is a pure function of its input, in one fixed order. A queue that
// reordered between two runs of one job is one a reviewer cannot resume.
func TestTheQueueIsTheSameEveryTime(t *testing.T) {
	res := widgets()
	res.Guesses = []alchemy.Guess{
		{Field: "b", ChosenAs: "y", Provenance: fromTable},
		{Field: "a", ChosenAs: "x", Provenance: fromTable},
	}
	opts := review.Options{Reviewing: true, MinConfidence: 0.9}
	first := queueOf(res, opts)
	for i := 0; i < 50; i++ {
		if !reflect.DeepEqual(first, queueOf(res, opts)) {
			t.Fatal("the queue is not stable across runs")
		}
	}
}
