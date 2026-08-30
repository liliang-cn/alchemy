package ontology_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

func loaded(t *testing.T, doc string) *ontology.Ontology {
	t.Helper()
	o, err := ontology.Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return o
}

const base = `{"id":"freight-ops@8","parts":{"prose":{
  "entities":[{"name":"Person"}],
  "relations":[]}}}`

// The loop this exists to close: a corpus says what the vocabulary is missing,
// a person accepts it, and the next run is governed by a vocabulary that has
// the word. Before this the only route was hand-editing JSON, which is the
// wrong order — the vocabulary is a claim about a corpus and the corpus is
// what tells you the claim is incomplete.
func TestAcceptingAProposalDeclaresTheTypeAndBumpsTheId(t *testing.T) {
	o := loaded(t, base)
	out, added, err := o.Extend("prose", []alchemy.Proposal{
		{Kind: alchemy.ProposalEntity, Type: "Team", Records: 1,
			Sources: []string{"liliang"}, Producers: []alchemy.Producer{alchemy.ProducerHuman}},
	}, "liliang", "")
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if out.ID != "freight-ops@9" {
		t.Errorf("new id = %q, want freight-ops@9: an ontology that gained a type is a different "+
			"vocabulary and Provenance.Ontology names it on every record", out.ID)
	}
	if len(added) != 1 || added[0] != "Team" {
		t.Errorf("added = %v, want [Team]", added)
	}
	v, err := out.Vocabulary("prose")
	if err != nil {
		t.Fatalf("Vocabulary: %v", err)
	}
	if !v.AllowsEntity("Team") {
		t.Fatal("the accepted type is not declared in the vocabulary it was accepted into")
	}
	// The origin sentence is what keeps a vocabulary from becoming a list
	// nobody can account for.
	for _, e := range v.Entities {
		if e.Name != "Team" {
			continue
		}
		for _, want := range []string{"liliang", "used 1 time"} {
			if !strings.Contains(e.Description, want) {
				t.Errorf("the accepted type's description is %q, want it to say %q", e.Description, want)
			}
		}
	}
	// The original is untouched: a caller holding one document and getting
	// another back can compare them, which they could not do if Extend edited
	// in place.
	if o.ID != "freight-ops@8" {
		t.Errorf("the source ontology's id became %q; Extend must not edit in place", o.ID)
	}
	if before, _ := o.Vocabulary("prose"); before.AllowsEntity("Team") {
		t.Error("the source vocabulary gained the type too; the two documents share a slice")
	}
}

// The rule that makes this safe. Load's own comment says an empty end is read
// as OPEN, so a relation accepted without ends would hold between anything —
// a rule widened silently at the moment somebody pressed Accept.
func TestARelationWithNoObservedEndsIsRefusedRatherThanDeclaredOpen(t *testing.T) {
	o := loaded(t, base)
	_, _, err := o.Extend("prose", []alchemy.Proposal{
		{Kind: alchemy.ProposalRelation, Type: "MEMBER_OF", Records: 4, From: []string{"Person"}},
	}, "liliang", "")
	if err == nil {
		t.Fatal("a relation whose `to` end was never observed was declared anyway; it now holds " +
			"between any two types in the vocabulary")
	}
	if !strings.Contains(err.Error(), "Accept the entity types first") {
		t.Errorf("the refusal does not say how to get past it: %v", err)
	}
}

// And once both ends are declared, the same proposal carries them and goes in.
func TestOnceBothEndsAreDeclaredTheRelationCarriesThem(t *testing.T) {
	o := loaded(t, `{"id":"freight-ops@9","parts":{"prose":{
	  "entities":[{"name":"Person"},{"name":"Team"}],
	  "relations":[]}}}`)
	out, _, err := o.Extend("prose", []alchemy.Proposal{
		{Kind: alchemy.ProposalRelation, Type: "MEMBER_OF", Records: 4,
			From: []string{"Person"}, To: []string{"Team"}},
	}, "liliang", "")
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	v, _ := out.Vocabulary("prose")
	if ok, why := v.AllowsRelation("MEMBER_OF", "Person", "Team"); !ok {
		t.Errorf("MEMBER_OF is not allowed from Person to Team after being accepted with those ends: %s", why)
	}
	// And it is not open: the ends were declared, so a pair nobody observed is
	// still a violation.
	if ok, _ := v.AllowsRelation("MEMBER_OF", "Team", "Person"); ok {
		t.Error("MEMBER_OF holds in a direction nobody observed; the ends were not applied")
	}
}

// Extending is a judgement about what a type means. §5c's argument about rules
// is the same one: a decision nobody signed cannot be argued with later.
func TestExtendingUnsignedIsRefused(t *testing.T) {
	o := loaded(t, base)
	if _, _, err := o.Extend("prose", []alchemy.Proposal{
		{Kind: alchemy.ProposalEntity, Type: "Team"},
	}, "", ""); err == nil {
		t.Fatal("an unsigned extension was accepted")
	}
}

// A version this package did not choose is not one it may guess the successor
// of. Asking for the id is one field; inventing a convention for somebody
// else's document is a decision nobody made.
func TestANonNumericVersionAsksForTheNewIdRatherThanInventingOne(t *testing.T) {
	o := loaded(t, `{"id":"sds@2026-08","parts":{"prose":{"entities":[{"name":"Person"}],"relations":[]}}}`)
	p := []alchemy.Proposal{{Kind: alchemy.ProposalEntity, Type: "Team"}}

	_, _, err := o.Extend("prose", p, "liliang", "")
	if err == nil {
		t.Fatal("a next id was invented for a versioning scheme this package does not own")
	}
	out, _, err := o.Extend("prose", p, "liliang", "sds@2026-09")
	if err != nil {
		t.Fatalf("Extend with an explicit id: %v", err)
	}
	if out.ID != "sds@2026-09" {
		t.Errorf("id = %q, want the one the caller gave", out.ID)
	}
}

// A document is what comes back, because that is what the caller supplies to
// the next job. An *Ontology whose parts are unexported is not something they
// can write to a file.
func TestTheExtendedOntologyRoundTripsThroughItsOwnDocument(t *testing.T) {
	o := loaded(t, base)
	out, _, err := o.Extend("prose", []alchemy.Proposal{
		{Kind: alchemy.ProposalEntity, Type: "Team"},
	}, "liliang", "")
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	doc, err := out.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	again := loaded(t, string(doc))
	if again.ID != out.ID {
		t.Errorf("round trip changed the id: %q -> %q", out.ID, again.ID)
	}
	v, _ := again.Vocabulary("prose")
	if !v.AllowsEntity("Team") {
		t.Error("the accepted type did not survive the document it was written into")
	}
}

const declared = `{"id":"freight-ops@10","parts":{"prose":{
  "entities":[{"name":"Person"},{"name":"Product"},{"name":"Platform"}],
  "relations":[{"name":"DEVELOPS","from":["Person"],"to":["Product"]}]}}}`

// The other half of a vocabulary gap, and the more dangerous one. Adding a
// type cannot make anything previously invalid valid; widening one does, for
// every record any producer writes from then on.
//
// Found by running a real fact through it: "Bruno also works in CloudStack"
// produced not an unknown type but a declared type used between ends it does
// not allow, and the first version of proposals had nothing to say about it.
func TestWideningADeclaredTypeRecordsWhatItRanBetweenBefore(t *testing.T) {
	o := loaded(t, declared)
	out, added, err := o.Extend("prose", []alchemy.Proposal{{
		Kind: alchemy.ProposalRelationEnds, Type: "DEVELOPS", Records: 1,
		From: []string{"Person"}, To: []string{"Platform"},
		DeclaredFrom: []string{"Person"}, DeclaredTo: []string{"Product"},
	}}, "liliang", "")
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if len(added) != 1 || added[0] != "DEVELOPS" {
		t.Fatalf("added = %v, want [DEVELOPS]", added)
	}
	v, _ := out.Vocabulary("prose")
	if ok, why := v.AllowsRelation("DEVELOPS", "Person", "Platform"); !ok {
		t.Errorf("the widened pair is still refused: %s", why)
	}
	// The original pair survives: a widening adds, it does not replace.
	if ok, why := v.AllowsRelation("DEVELOPS", "Person", "Product"); !ok {
		t.Errorf("widening lost the pair that was already declared: %s", why)
	}
	for _, r := range v.Relations {
		if r.Name != "DEVELOPS" {
			continue
		}
		for _, want := range []string{"widened by liliang", "Person -> Product"} {
			if !strings.Contains(r.Description, want) {
				t.Errorf("the widened type does not record what it ran between before: %q", r.Description)
			}
		}
	}
	// A type that is not declared is not a widening, and saying so is the
	// difference between adding a rule and changing one.
	if _, _, err := o.Extend("prose", []alchemy.Proposal{{
		Kind: alchemy.ProposalRelationEnds, Type: "MANAGES", From: []string{"Person"}, To: []string{"Product"},
	}}, "liliang", ""); err == nil {
		t.Error("a widening was accepted for a type this vocabulary does not declare")
	}
}

// The trap in the other direction. An end declared empty is OPEN, so unioning
// what one corpus happened to show into it would narrow the rule — a different
// decision, made as a side effect of pressing Accept.
func TestAnEndThatIsAlreadyOpenIsNotNarrowedByAcceptingAWidening(t *testing.T) {
	o := loaded(t, `{"id":"x@1","parts":{"prose":{
	  "entities":[{"name":"Person"},{"name":"Platform"}],
	  "relations":[{"name":"TOUCHES","from":["Person"]}]}}}`)
	_, _, err := o.Extend("prose", []alchemy.Proposal{{
		Kind: alchemy.ProposalRelationEnds, Type: "TOUCHES",
		From: []string{"Person"}, To: []string{"Platform"},
	}}, "liliang", "")
	if err == nil {
		t.Fatal("an open end was narrowed to what one corpus showed, in the name of widening it")
	}
	if !strings.Contains(err.Error(), "narrow") {
		t.Errorf("the refusal does not say which direction the rule would have moved: %v", err)
	}
}
