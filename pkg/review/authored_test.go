package review_test

import (
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/review"
)

var stated = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// flagPolicy is §6's own sentence written as policy up front: "--flag is never
// an entity". It is the shape the queue would have built for such a finding,
// which is the floor an authored shape has to reach.
func flagPolicy() review.Authorship {
	return review.Authorship{
		Shape:   "violation/unknown_entity_type/type=Flag/producer=llm-extract/model=gemini-3.6-flash-high",
		Verb:    review.VerbReject,
		By:      "ana@example.com",
		Because: "--verbose is a command-line switch, not an entity; the manuals are full of them",
		At:      stated,
	}
}

// §5c: "a rule is recorded with the decision that produced it, so a later
// reader can see why the rule exists … a rule whose origin expired with it is
// back to being an unexplainable policy."
//
// An authored rule has no decision to point at, so the requirement lands on
// what it does have: who declared it, why, and when. Each is required, because
// a rule missing any one of them is the policy §5c refuses — "six months on,
// the only available reading is that somebody must have had a reason."
func TestAnAuthoredRuleMustBeAbleToExplainItself(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*review.Authorship)
		want   string
	}{
		{"no author", func(a *review.Authorship) { a.By = "" }, "nobody"},
		{"no reason", func(a *review.Authorship) { a.Because = "" }, "reason"},
		{"no date", func(a *review.Authorship) { a.At = time.Time{} }, "when"},
		{"no shape", func(a *review.Authorship) { a.Shape = "" }, "shape"},
		{"no verb", func(a *review.Authorship) { a.Verb = "" }, "verb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := flagPolicy()
			tc.mutate(&a)
			_, err := a.Rule()
			if err == nil {
				t.Fatalf("Rule() = nil error for a rule with %s; an unexplainable policy is what §5c refuses", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to name what is missing (%q)", err, tc.want)
			}
		})
	}
}

// A well-formed authored rule keeps everything a later reader needs, and says
// it was authored rather than decided.
func TestAnAuthoredRuleCarriesItsAuthorReasonAndDate(t *testing.T) {
	r, err := flagPolicy().Rule()
	if err != nil {
		t.Fatalf("Rule: %v", err)
	}
	if r.Origin != review.OriginAuthored {
		t.Errorf("origin = %q, want %q", r.Origin, review.OriginAuthored)
	}
	if r.From.By != "ana@example.com" || r.From.Verb != review.VerbReject {
		t.Errorf("from = %+v, want the declaration itself", r.From)
	}
	if !r.From.At.Equal(stated) {
		t.Errorf("at = %v, want the date it was declared", r.From.At)
	}
	if !strings.Contains(r.Because, "command-line switch") {
		t.Errorf("because = %q, want the stated reason", r.Because)
	}
	// Kind is derived from the shape rather than asked for twice: a rule whose
	// declared kind disagreed with its shape would be two answers to one
	// question, and the shape is the half that decides what the rule matches.
	if r.Kind != review.KindViolation {
		t.Errorf("kind = %q, want it derived from the shape", r.Kind)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// §7.3 is the sharpest constraint on an authored rule. Its escape hatch —
// "tell the service how to resolve conflicts of that shape next time" — is a
// person who has *seen* one generalising from it, and the sentence ends "which
// is how a pipeline that started attended becomes one that runs itself without
// ever having guessed". An authored conflict rule is a guess by construction:
// nobody has seen the two claims it adjudicates between.
func TestAnAuthoredRuleMayNotPreAnswerAConflict(t *testing.T) {
	_, err := review.Authorship{
		Shape:   "conflict/entity_type/between=ddl|llm-extract/model=gemini-3.6-flash-high",
		Verb:    review.VerbAlways,
		By:      "ana@example.com",
		Because: "the schema always wins over the model on entity type",
		At:      stated,
	}.Rule()
	if err == nil {
		t.Fatal("an authored conflict rule was accepted; §7.3 refuses to let a caller opt out of a person")
	}
	if !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "§7.3") {
		t.Fatalf("err = %q, want it to name the conflict and the section that refuses it", err)
	}
}

// And the refusal is in the matching as well as in the validator, so that no
// route into the process — a file, the wire, a Go caller building a Rule
// literal — can quietly resolve a conflict from a declaration.
func TestAnAuthoredConflictRuleCoversNoConflictEvenIfOneIsBuiltByHand(t *testing.T) {
	shape := "conflict/entity_type/between=ddl|llm-extract/model=gemini-3.6-flash-high"
	authored := review.Rule{
		Shape: shape, Kind: review.KindConflict, Origin: review.OriginAuthored,
		From:    review.Decision{Verb: review.VerbAlways, By: "ana", At: stated},
		Because: "the schema always wins",
	}
	reviewed := review.Rule{
		Shape: shape, Kind: review.KindConflict,
		From:    review.Decision{Verb: review.VerbAlways, By: "ana", ItemID: "conflict/entity_type/n1"},
		Because: `entity "n1" is typed twice`,
	}
	res := conflicted()
	items := queueOf(res, review.Options{})
	if len(items) != 1 {
		t.Fatalf("queue = %+v, want the one conflict", items)
	}
	if authored.Covers(items[0]) {
		t.Error("an authored rule covers a conflict; §7.3's person would have been opted out of by a declaration")
	}
	// The reviewer's rule of the same shape still covers it — the difference
	// is the warrant, not the shape.
	if !reviewed.Covers(items[0]) {
		t.Error("a reviewer's rule no longer covers the conflict it was made from")
	}
}

// A conflict rule an operator wrote by hand does not merely fail to match: the
// job it was supposed to unblock is still held, which is §7.3's sentence
// staying literally true.
func TestAJobStaysHeldDespiteAnAuthoredConflictRule(t *testing.T) {
	res := conflicted()
	authored := review.Rule{
		Shape: "conflict/entity_type/between=ddl|llm-extract/model=gemini-3.6-flash-high",
		Kind:  review.KindConflict, Origin: review.OriginAuthored,
		From:    review.Decision{Verb: review.VerbAlways, By: "ana", At: stated},
		Because: "the schema always wins",
	}
	items := queueOf(res, review.Options{Rules: []review.Rule{authored}})
	got, _, err := review.Apply(res, items, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if open := review.Held(got); len(open) != 1 {
		t.Fatalf("held = %+v, want the conflict still holding the job", open)
	}
}

// The floor on width. A reviewer's rule is bounded by a finding that existed —
// shapeOf builds it from a real item — and an authored one has no such floor,
// so the widest shape a person may write is one the queue itself could have
// produced: one named producer, one named class from it.
func TestTheWidestAuthoredShapeStillNamesOneProducerAndOneClass(t *testing.T) {
	// Wide, and legal: every unsure DEPLOYED_ON edge this model proposes, in
	// any corpus, forever. That is a claim an operator can hold and defend.
	wide := review.Authorship{
		Shape:   "low_confidence/relation/type=DEPLOYED_ON/producer=llm-extract/model=gemini-3.6-flash-high",
		Verb:    review.VerbAlways,
		By:      "ana@example.com",
		Because: "this model's unsure DEPLOYED_ON edges have always been right for us",
		At:      stated,
	}
	if _, err := wide.Rule(); err != nil {
		t.Fatalf("a wide but bounded rule was refused: %v", err)
	}

	// One step wider is the rule that turns review off while leaving it
	// switched on: every unsure record this model proposes, of any type.
	for _, tc := range []struct{ name, shape string }{
		{"no type", "low_confidence/relation/type=/producer=llm-extract/model=gemini-3.6-flash-high"},
		{"no producer", "low_confidence/relation/type=DEPLOYED_ON/producer="},
		{"no class at all", "violation/unknown_entity_type/type=/producer=llm-extract"},
		{"a kind the queue never builds", "everything/type=Widget/producer=llm-extract"},
		{"an invented segment", "violation/unknown_entity_type/type=Widget/producer=llm-extract/chunk=41"},
		{"a violation kind nobody raises", "violation/looks_wrong/type=Widget/producer=llm-extract"},
		{"a producer nobody has", "violation/unknown_entity_type/type=Widget/producer=intern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := wide
			a.Shape = tc.shape
			if _, err := a.Rule(); err == nil {
				t.Fatalf("shape %q was accepted; a rule that widens without a floor is review switched off while the flag says it is on", tc.shape)
			}
		})
	}
}

// §5c's `always` means "accept this and stop asking about ones like it". A
// person writing policy up front most often wants the opposite — §6's own
// sentence, "--flag is never an entity", is a drop — so an authored rule may
// carry a rejection. The two verbs that cannot be a rule are the two that are
// about one record: accept and edit say something about the item in front of
// the reviewer, and a rule is by definition about a class.
func TestAnAuthoredRuleMayRejectButMayNotAcceptOneRecord(t *testing.T) {
	for _, tc := range []struct {
		verb review.Verb
		ok   bool
	}{
		{review.VerbReject, true},
		{review.VerbAlways, true},
		{review.VerbAccept, false},
		{review.VerbEdit, false},
		{review.Verb("shrug"), false},
	} {
		a := flagPolicy()
		a.Verb = tc.verb
		_, err := a.Rule()
		if (err == nil) != tc.ok {
			t.Errorf("verb %q: err = %v, want ok = %v", tc.verb, err, tc.ok)
		}
	}
}

// The rule file is what a nightly pipeline carries instead of a person. It is
// refused as a whole when any rule in it cannot explain itself, because a
// policy file half of which was ignored is worse than one that was rejected.
func TestARuleFileIsLoadedOrRefusedWhole(t *testing.T) {
	const good = `{
	  "rules": [
	    {
	      "shape": "violation/unknown_entity_type/type=Flag/producer=llm-extract/model=gemini-3.6-flash-high",
	      "verb": "reject",
	      "by": "ana@example.com",
	      "because": "--flag is a command-line switch, not an entity",
	      "at": "2026-08-01T09:00:00Z"
	    },
	    {
	      "shape": "low_confidence/entity/type=Cluster/producer=llm-extract/model=gemini-3.6-flash-high",
	      "verb": "always",
	      "by": "ana@example.com",
	      "because": "this model has never been wrong about a cluster in this corpus",
	      "at": "2026-08-01T09:00:00Z",
	      "note": "revisit when the model is swapped"
	    }
	  ]
	}`
	rules, err := review.LoadRules(strings.NewReader(good))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
	if rules[0].Origin != review.OriginAuthored || rules[0].From.Verb != review.VerbReject {
		t.Errorf("rule[0] = %+v, want an authored rejection", rules[0])
	}

	const bad = `{"rules":[{"shape":"violation/unknown_entity_type/type=Flag/producer=llm-extract","verb":"reject","by":"ana"}]}`
	if _, err := review.LoadRules(strings.NewReader(bad)); err == nil {
		t.Fatal("a rule with no stated reason was loaded; a rule with an empty reason is the unexplainable policy §5c names")
	}
	if _, err := review.LoadRules(strings.NewReader(`{"rules":[`)); err == nil {
		t.Fatal("a malformed file was loaded")
	}
}

// A reviewer's rule is still the default reading of a rule that does not say.
// Every rule that could exist before this field did was minted from a decision
// on an item, so silence has to mean what those rules already meant — and an
// authored one has to say so, which is the direction that fails safe.
func TestARuleThatDoesNotSayWhereItCameFromIsAReviewersRule(t *testing.T) {
	r := review.Rule{
		Shape: "violation/unknown_entity_type/type=Widget/producer=llm-extract/model=gemini-3.6-flash-high",
		Kind:  review.KindViolation,
		From:  review.Decision{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbAlways, By: "ana"},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if r.Authored() {
		t.Error("a rule with no origin reads as authored; the wire default has to mean what the callers who wrote it meant")
	}
	// And a reviewer's rule that nobody signed is refused for §5c's original
	// reason: a decision nobody signed cannot be written into provenance.
	unsigned := r
	unsigned.From.By = ""
	if err := unsigned.Validate(); err == nil {
		t.Error("a rule whose decision names nobody was accepted")
	}
}

// §5's obligation is the numbers needed to distrust a graph, and a rule that
// may now say "reject" is the one thing that can take a record out of one
// without anybody seeing it. So it is counted — and a record a person threw
// away is deliberately not, because that number would conflate a judgement
// made on a record somebody read with a policy applied to a record nobody did.
func TestRecordsARuleRemovedAreCountedAndAPersonsRejectionIsNot(t *testing.T) {
	rule := review.Rule{
		Shape:  "violation/unknown_entity_type/type=Widget/producer=llm-extract/model=gemini-3.6-flash-high",
		Kind:   review.KindViolation,
		Origin: review.OriginAuthored,
		From: review.Decision{
			Verb: review.VerbReject, By: "ana@example.com", At: stated,
		},
		Because: "widgets are not a thing in this corpus",
	}
	res := widgets()
	items := queueOf(res, review.Options{Reviewing: true, Rules: []review.Rule{rule}})

	// Nobody is asked: this is the unattended run the rule was written for.
	got, _, err := review.Apply(res, items, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("entities = %+v, want the two Widgets dropped and the Gadget kept", got.Entities)
	}
	if got.Counts.Dropped != 2 {
		t.Errorf("counts.dropped = %d, want the two records the rule removed", got.Counts.Dropped)
	}

	// The same two records, thrown away by a person who read them, are not in
	// that number: they are reported by the reviewer's name on the finding.
	byHand, _, err := review.Apply(res, queueOf(res, review.Options{Reviewing: true}), []review.Decision{
		{ItemID: "violation/unknown_entity_type/w1", Verb: review.VerbReject, By: "bo"},
		{ItemID: "violation/unknown_entity_type/w2", Verb: review.VerbReject, By: "bo"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(byHand.Entities) != 1 {
		t.Fatalf("entities = %+v, want the two the reviewer rejected gone", byHand.Entities)
	}
	if byHand.Counts.Dropped != 0 {
		t.Errorf("counts.dropped = %d, want none: somebody read every one of these", byHand.Counts.Dropped)
	}
}
