package review_test

import (
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

var (
	chunk1 = alchemy.Provenance{
		Source: "design.md", Chunk: 1, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.7-flash-high", Confidence: 0.8,
	}
	chunk2 = alchemy.Provenance{
		Source: "design.md", Chunk: 2, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.7-flash-high", Confidence: 0.8,
	}
)

// dup is the graph the run produced: one package under two names, and an edge
// on each of them, so that what happens to the edges of an absorbed node is
// visible rather than asserted about an empty node.
func dup() (verify.Report, alchemy.Result) {
	entities := []alchemy.Entity{
		{ID: "package:document", Type: "Package", Name: "document", Provenance: chunk1},
		{ID: "package:document package", Type: "Package", Name: "document package",
			Attributes: map[string]any{"section": "2"}, Provenance: chunk2},
		{ID: "format:sql", Type: "Format", Name: "SQL", Provenance: chunk1},
	}
	relations := []alchemy.Relation{
		{From: "package:document", To: "format:sql", Type: "READS", Provenance: chunk1},
		{From: "package:document package", To: "format:sql", Type: "READS", Provenance: chunk2},
		{From: "package:document package", To: "package:document", Type: "MENTIONS", Provenance: chunk2},
	}
	finding := alchemy.Duplicate{
		Signal:  alchemy.DuplicateNameAffix,
		Subject: "package:document ~ package:document package",
		Detail:  `Package "document" per design.md chunk 1 and Package "document package" per design.md chunk 2 differ only by the trailing "package"`,
		Left: alchemy.DuplicateSide{ID: "package:document", Type: "Package",
			Name: "document", Provenance: chunk1},
		Right: alchemy.DuplicateSide{ID: "package:document package", Type: "Package",
			Name: "document package", Provenance: chunk2},
	}
	rep := verify.Report{Entities: entities, Relations: relations,
		Duplicates: []alchemy.Duplicate{finding}}
	res := alchemy.Result{Entities: entities, Relations: relations,
		Duplicates: []alchemy.Duplicate{finding}}
	return rep, res
}

// §5c's table decides the rank, and its logic decides where a new kind goes.
// A duplicate is below a violation because nothing is wrong: both nodes are
// well-typed and attributable. It is below a guess because a wrong guess
// misaligns a whole table while an unanswered duplicate leaves the graph
// exactly as the extractor produced it, which is the state the result already
// reports a number for. It is above low confidence because the answer decides
// what every edge on both nodes points at, where an unsure edge is one edge.
func TestADuplicateRanksBelowAGuessAndAboveLowConfidence(t *testing.T) {
	rep, res := dup()
	rep.Violations = []alchemy.Violation{{
		Kind: alchemy.ViolationUnknownEntityType, Subject: "format:sql",
		Detail: "type not declared", Provenance: chunk1,
	}}
	res.Violations = rep.Violations
	res.Guesses = []alchemy.Guess{{Field: "pkg", ChosenAs: "package.name", Provenance: fromTable}}

	got := review.Queue(rep, res, review.Options{Reviewing: true, MinConfidence: 0.9})

	var kinds []review.Kind
	for _, it := range got {
		kinds = append(kinds, it.Kind)
	}
	want := []review.Kind{review.KindViolation, review.KindGuess, review.KindDuplicate}
	if len(kinds) < len(want) {
		t.Fatalf("kinds = %v, want at least %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v then the low-confidence records", kinds, want)
		}
	}
	if kinds[len(want)] != review.KindLowConfidence {
		t.Fatalf("kinds = %v, want low confidence after the duplicate", kinds)
	}
}

// §7.3's table, one row wider. A duplicate is not a conflict: the two records
// agree, so an unattended caller is owed the finding and the number, not a
// held job. Holding here would stop every prose import — one node in six was
// one of these — which is how a refusal that matters becomes a dialog people
// click through.
func TestDuplicatesAreNotQueuedWithoutReviewAndNeverHoldTheJob(t *testing.T) {
	rep, res := dup()

	if got := review.Queue(rep, res, review.Options{}); len(got) != 0 {
		t.Fatalf("queue = %+v, want nothing: a caller who did not ask for review gets the finding, not the question", got)
	}
	if held := res.Held(); len(held) != 0 {
		t.Fatalf("held = %+v, want nothing: two records that agree are not two sources claiming to be right", held)
	}
}

// The decided merge, end to end. `edit` names the node this record is the same
// as; nothing else in the graph is allowed to be left behind by it.
func TestAMergeRedirectsTheEdgesAndKeepsBothProvenances(t *testing.T) {
	rep, res := dup()
	items := review.Queue(rep, res, review.Options{Reviewing: true})
	it := items[0]

	out, _, err := review.Apply(res, items, []review.Decision{{
		ItemID: it.ID, Verb: review.VerbEdit, By: "ana",
		Edit: &review.Edit{Into: "package:document"},
		Note: "chunk 2 wrote the type word into the name",
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if ids := entityIDs(out); len(ids) != 2 || ids[0] != "package:document" || ids[1] != "format:sql" {
		t.Fatalf("entities = %q, want the absorbed node gone and the rest in place", ids)
	}
	// Every edge that pointed at the absorbed id points at the survivor, and
	// the edge that was between the two is gone rather than left as a loop.
	want := []string{"package:document -[READS]-> format:sql"}
	if got := relationStrings(out); !equal(got, want) {
		t.Fatalf("relations = %q, want %q", got, want)
	}
	// The absorbed node stated something the survivor did not; losing it would
	// make the merge a deletion.
	if out.Entities[0].Attributes["section"] != "2" {
		t.Fatalf("survivor attributes = %v, want the absorbed node's section", out.Entities[0].Attributes)
	}
	// §5b: the absorbed node's provenance is not lost — the finding stays in
	// the result, carrying both chunks and the name of whoever decided it.
	if len(out.Duplicates) != 1 {
		t.Fatalf("duplicates = %+v, want the finding kept", out.Duplicates)
	}
	d := out.Duplicates[0]
	if d.Left.Provenance.ReviewedBy != "ana" || d.Right.Provenance.ReviewedBy != "ana" {
		t.Fatalf("finding reviewed by %q and %q, want both sides to name ana",
			d.Left.Provenance.ReviewedBy, d.Right.Provenance.ReviewedBy)
	}
	if d.Right.Provenance.Chunk != 2 {
		t.Fatalf("the absorbed side cites chunk %d, want the chunk that proposed it", d.Right.Provenance.Chunk)
	}
	if out.Counts.Entities != 2 || out.Counts.Relations != 1 || out.Counts.Duplicates != 1 {
		t.Fatalf("counts = %+v, want them to describe the merged graph", out.Counts)
	}
}

// A merge is not a deletion and must not be dressed as one. `reject` on any
// other kind removes the record it names; here it names two nodes, one of
// which the reviewer means to keep, and there is no reading of "reject" that
// does not lose a node somebody wanted.
func TestRejectingADuplicateIsRefusedRatherThanDeletingANode(t *testing.T) {
	rep, res := dup()
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	_, _, err := review.Apply(res, items, []review.Decision{{
		ItemID: items[0].ID, Verb: review.VerbReject, By: "ana",
	}})
	if err == nil {
		t.Fatal("rejecting a duplicate was accepted; it would have deleted both nodes")
	}
	if !strings.Contains(err.Error(), "accept") || !strings.Contains(err.Error(), "into") {
		t.Fatalf("error %q does not say what to press instead", err)
	}
}

// Accepting says they are two things, and the graph is left exactly as the
// extractor produced it — with both nodes carrying the reviewer's name, so a
// later reader can tell "looked at and kept apart" from "nobody looked".
func TestAcceptingADuplicateKeepsBothNodes(t *testing.T) {
	rep, res := dup()
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	out, _, err := review.Apply(res, items, []review.Decision{{
		ItemID: items[0].ID, Verb: review.VerbAccept, By: "ana",
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(out.Entities) != 3 || len(out.Relations) != 3 {
		t.Fatalf("graph = %d entities, %d relations; want it untouched", len(out.Entities), len(out.Relations))
	}
	for _, e := range out.Entities[:2] {
		if e.Provenance.ReviewedBy != "ana" {
			t.Fatalf("entity %q reviewed by %q, want both sides stamped", e.ID, e.Provenance.ReviewedBy)
		}
	}
}

// §5c's `always`, which is the whole reason this is a queue item and not a
// line in a report: the nightly re-import must not ask again. The rule the
// decision mints has to match tomorrow's finding — same pair, same producer —
// and it has to carry the merge, or "stop asking" would mean "stop merging".
func TestAnAlwaysOnAMergeAnswersTheSamePairOnTheNextRun(t *testing.T) {
	rep, res := dup()
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	_, rules, err := review.Apply(res, items, []review.Decision{{
		ItemID: items[0].ID, Verb: review.VerbAlways, By: "ana",
		Edit: &review.Edit{Into: "package:document"},
		Note: "the model writes the type word into the name",
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want the one `always` recorded", rules)
	}

	// Tomorrow: the same corpus, re-chunked, so the same pair arrives from
	// different chunks. The rule still covers it, and nobody is asked.
	tomorrowRep, tomorrow := dup()
	tomorrowRep.Duplicates[0].Left.Provenance.Chunk = 7
	tomorrowRep.Duplicates[0].Right.Provenance.Chunk = 9
	again := review.Queue(tomorrowRep, tomorrow, review.Options{Reviewing: true, Rules: rules})
	if len(again) != 1 {
		t.Fatalf("queue = %+v, want the one item", again)
	}
	if again[0].SuppressedBy == nil {
		t.Fatalf("item %q was asked again; the rule %q should have answered it", again[0].Shape, rules[0].Shape)
	}
	if len(review.Open(again)) != 0 {
		t.Fatalf("open = %+v, want nothing left for a person", review.Open(again))
	}
}

// The shape is what the rule matches on, and for this kind it keeps both
// names. Everywhere else a shape drops the instance because the class is what
// was decided; here the two names *are* the class — the reviewer said these
// two spellings are one thing, and nothing they said covers a third pair they
// have never seen. A shape that generalised over the names would merge nodes
// on the strength of a decision about different nodes.
func TestADuplicateRuleDoesNotCoverADifferentPair(t *testing.T) {
	rep, res := dup()
	items := review.Queue(rep, res, review.Options{Reviewing: true})
	_, rules, err := review.Apply(res, items, []review.Decision{{
		ItemID: items[0].ID, Verb: review.VerbAlways, By: "ana",
		Edit: &review.Edit{Into: "package:document"},
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	otherRep, other := dup()
	otherRep.Duplicates[0] = alchemy.Duplicate{
		Signal:  alchemy.DuplicateNameAffix,
		Subject: "package:ontology ~ package:ontology package",
		Left: alchemy.DuplicateSide{ID: "package:ontology", Type: "Package",
			Name: "ontology", Provenance: chunk1},
		Right: alchemy.DuplicateSide{ID: "package:ontology package", Type: "Package",
			Name: "ontology package", Provenance: chunk2},
	}
	other.Duplicates = otherRep.Duplicates

	got := review.Queue(otherRep, other, review.Options{Reviewing: true, Rules: rules})
	if len(got) != 1 {
		t.Fatalf("queue = %+v, want the one item", got)
	}
	if got[0].SuppressedBy != nil {
		t.Fatalf("a decision about document/document package answered %q", got[0].Subject)
	}
}

// §5c's operator who already knows their corpus, writing the policy before the
// first job. It is allowed here and refused for conflicts, and the difference
// is what the shape is bounded by: an authored conflict rule says "whenever
// these two producers disagree, forever", having seen no instance, while this
// one names both spellings and can only ever act on the pair it spells out.
func TestAnOperatorCanWriteAMergeRuleBeforeTheFirstJob(t *testing.T) {
	a := review.Authorship{
		Shape:   `duplicate/name_affix/type=Package/left=document/right=document package/between=llm-extract|llm-extract/model=gemini-3.7-flash-high`,
		Verb:    review.VerbAlways,
		By:      "ops",
		Because: "this corpus's model writes the type word into the name, and we always want the short one",
		At:      time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
		Edit:    &review.Edit{Into: "package:document"},
	}
	rule, err := a.Rule()
	if err != nil {
		t.Fatalf("Authorship.Rule: %v", err)
	}

	rep, res := dup()
	got := review.Queue(rep, res, review.Options{Reviewing: true, Rules: []review.Rule{rule}})
	if len(got) != 1 || got[0].SuppressedBy == nil {
		t.Fatalf("queue = %+v, want the authored rule to have answered it", got)
	}
	out, _, err := review.Apply(res, got, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ids := entityIDs(out); len(ids) != 2 {
		t.Fatalf("entities = %q, want the rule to have merged the pair unattended", ids)
	}
}

// The floor authoredShape puts under every hand-written rule, applied to this
// kind: a rule that names one side and not the other would merge every future
// node whose name contains "document" into whatever it liked.
func TestAnAuthoredMergeRuleMustNameBothSides(t *testing.T) {
	a := review.Authorship{
		Shape:   `duplicate/name_affix/type=Package/left=document/between=llm-extract|llm-extract`,
		Verb:    review.VerbAlways,
		By:      "ops",
		Because: "merge them",
		At:      time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
		Edit:    &review.Edit{Into: "package:document"},
	}
	if _, err := a.Rule(); err == nil {
		t.Fatal("a merge rule naming one side was accepted")
	}
}

func entityIDs(res alchemy.Result) []string {
	var out []string
	for _, e := range res.Entities {
		out = append(out, e.ID)
	}
	return out
}

func relationStrings(res alchemy.Result) []string {
	var out []string
	for _, r := range res.Relations {
		out = append(out, r.From+" -["+r.Type+"]-> "+r.To)
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Three spellings of one thing, answered as two findings. A reviewer being
// consistent must not be a reviewer being refused: the chain is followed to
// the node nothing absorbs, and everything lands on it whichever order the two
// answers arrived in.
func TestAChainOfMergesLandsEverythingOnTheNodeNothingAbsorbs(t *testing.T) {
	entities := []alchemy.Entity{
		{ID: "package:doc", Type: "Package", Name: "doc", Provenance: chunk1},
		{ID: "package:doc package", Type: "Package", Name: "doc package", Provenance: chunk2},
		{ID: "package:doc package v2", Type: "Package", Name: "doc package v2", Provenance: chunk2},
	}
	pair := func(l, r alchemy.Entity) alchemy.Duplicate {
		return alchemy.Duplicate{
			Signal: alchemy.DuplicateNameAffix, Subject: l.ID + " ~ " + r.ID,
			Left:  alchemy.DuplicateSide{ID: l.ID, Type: l.Type, Name: l.Name, Provenance: l.Provenance},
			Right: alchemy.DuplicateSide{ID: r.ID, Type: r.Type, Name: r.Name, Provenance: r.Provenance},
		}
	}
	relations := []alchemy.Relation{
		{From: "package:doc package v2", To: "format:sql", Type: "READS", Provenance: chunk2},
	}
	res := alchemy.Result{Entities: entities, Relations: relations,
		Duplicates: []alchemy.Duplicate{pair(entities[0], entities[1]), pair(entities[1], entities[2])}}
	rep := verify.Report{Entities: entities, Relations: relations, Duplicates: res.Duplicates}
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	decisions := []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbEdit, By: "ana", Edit: &review.Edit{Into: "package:doc"}},
		{ItemID: items[1].ID, Verb: review.VerbEdit, By: "ana", Edit: &review.Edit{Into: "package:doc package"}},
	}
	for _, order := range [][]review.Decision{decisions, {decisions[1], decisions[0]}} {
		out, _, err := review.Apply(res, items, order)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if ids := entityIDs(out); len(ids) != 1 || ids[0] != "package:doc" {
			t.Fatalf("entities = %q, want everything on the node nothing absorbs", ids)
		}
		if got := relationStrings(out); !equal(got, []string{"package:doc -[READS]-> format:sql"}) {
			t.Fatalf("relations = %q, want the edge carried to the survivor", got)
		}
	}
}

// A merge into a node the reviewer was not shown is refused. It is the one way
// an answer to a narrow question could become a licence to rewrite the graph,
// and when the answer comes from a standing rule it is a licence used on a
// night nobody is watching.
func TestMergingIntoANodeTheItemDoesNotNameIsRefused(t *testing.T) {
	rep, res := dup()
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	_, _, err := review.Apply(res, items, []review.Decision{{
		ItemID: items[0].ID, Verb: review.VerbEdit, By: "ana",
		Edit: &review.Edit{Into: "format:sql"},
	}})
	if err == nil {
		t.Fatal("a merge into an unrelated node was accepted")
	}
	if !strings.Contains(err.Error(), "format:sql") {
		t.Fatalf("error %q does not name the node it refused", err)
	}
}

// `into` on anything but a duplicate is refused for the same reason. A
// violation names one record and says what is wrong with it; a decision on it
// that moved records into another node would be acting on a question nobody
// asked.
func TestAMergeOnAViolationIsRefused(t *testing.T) {
	rep, res := dup()
	rep.Duplicates = nil
	res.Duplicates = nil
	rep.Violations = []alchemy.Violation{{
		Kind: alchemy.ViolationUnknownEntityType, Subject: "package:document package",
		Detail: "type not declared", Provenance: chunk2,
	}}
	res.Violations = rep.Violations
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	_, _, err := review.Apply(res, items, []review.Decision{{
		ItemID: items[0].ID, Verb: review.VerbEdit, By: "ana",
		Edit: &review.Edit{Into: "package:document"},
	}})
	if err == nil {
		t.Fatal("a violation was answered with a merge")
	}
}

// Apply promises not to modify what it was given, and a merge is the one
// operation that writes into an attribute map somebody else is holding.
func TestAMergeDoesNotModifyTheResultItWasGiven(t *testing.T) {
	rep, res := dup()
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	if _, _, err := review.Apply(res, items, []review.Decision{{
		ItemID: items[0].ID, Verb: review.VerbEdit, By: "ana",
		Edit: &review.Edit{Into: "package:document"},
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Entities) != 3 || res.Entities[0].Attributes != nil {
		t.Fatalf("the input result changed underneath its holder: %+v", res.Entities)
	}
	if res.Duplicates[0].Left.Provenance.ReviewedBy != "" {
		t.Fatalf("the input finding was stamped in place: %+v", res.Duplicates[0])
	}
}
