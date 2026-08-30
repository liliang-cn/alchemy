package verify_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// team is the case this was built for, in miniature: a person states four
// people, a team, and who leads it, under a vocabulary that has a word for
// Person and none for Team.
func team() verify.Input {
	who := alchemy.Provenance{Source: "liliang", Chunk: -1, Producer: alchemy.ProducerHuman, By: "liliang"}
	in := verify.Input{
		OntologyID: "freight-ops@8",
		Vocabulary: ontology.Vocabulary{
			Entities: []ontology.EntityType{{Name: "Person"}},
		},
		Entities: []alchemy.Entity{
			{ID: "team:ravel", Type: "Team", Name: "Ravel Team", Provenance: who},
		},
	}
	for _, name := range []string{"elena", "bruno", "robert", "iris"} {
		in.Entities = append(in.Entities, alchemy.Entity{
			ID: "person:" + name, Type: "Person", Name: name, Provenance: who,
		})
		in.Relations = append(in.Relations, alchemy.Relation{
			From: "person:" + name, To: "team:ravel", Type: "MEMBER_OF",
			Key: "m-" + name, Provenance: who,
		})
	}
	in.Relations = append(in.Relations, alchemy.Relation{
		From: "person:elena", To: "team:ravel", Type: "LEADS", Key: "lead", Provenance: who,
	})
	return in
}

// TestOneUndeclaredTypeIsOneProposalHoweverManyRecordsUsedIt is the whole
// difference from a violation.
//
// The same input produces six violations — one per record — which is the right
// shape for "what is wrong with this record" and the wrong shape for "what is
// missing from the vocabulary". A four-hundred-thousand-record import missing
// one type produces four hundred thousand of the first and one of the second.
func TestOneUndeclaredTypeIsOneProposalHoweverManyRecordsUsedIt(t *testing.T) {
	rep := verify.Check(team())

	if len(rep.Violations) != 6 {
		t.Fatalf("violations = %d, want 6 (one per record); this test's premise is wrong", len(rep.Violations))
	}
	if len(rep.Proposals) != 3 {
		t.Fatalf("proposals = %d, want 3 — Team, MEMBER_OF and LEADS, once each\n%+v",
			len(rep.Proposals), rep.Proposals)
	}
	// Entity types first, so a list accepted top to bottom never has a line
	// referring to something further down.
	if rep.Proposals[0].Kind != alchemy.ProposalEntity || rep.Proposals[0].Type != "Team" {
		t.Errorf("first proposal is %+v, want the entity type the relations name", rep.Proposals[0])
	}
	for _, p := range rep.Proposals {
		if p.Type == "MEMBER_OF" && p.Records != 4 {
			t.Errorf("MEMBER_OF was used 4 times and the proposal says %d; the count is what "+
				"tells a vocabulary gap from a typo", p.Records)
		}
	}
}

// A proposal says what to do, and for a relation that means the ends. They are
// observed and not inferred: this records that MEMBER_OF ran from Person to
// something, which is a fact about the corpus, and stops short of saying what
// the type means.
func TestARelationProposalNamesTheEndsItWasActuallyUsedBetween(t *testing.T) {
	rep := verify.Check(team())

	var member alchemy.Proposal
	for _, p := range rep.Proposals {
		if p.Type == "MEMBER_OF" {
			member = p
		}
	}
	if len(member.From) != 1 || member.From[0] != "Person" {
		t.Errorf("MEMBER_OF proposal From = %v, want [Person]", member.From)
	}
	// Team is itself undeclared, so it is not named as an end. A line
	// proposing two undeclared things at once is a line nobody can accept or
	// reject as one thing.
	if len(member.To) != 0 {
		t.Errorf("MEMBER_OF proposal To = %v, want nothing: Team is undeclared too and is its "+
			"own proposal", member.To)
	}
	if len(member.Producers) != 1 || member.Producers[0] != alchemy.ProducerHuman {
		t.Errorf("proposal producers = %v, want the human who asserted it: §5b's question asked "+
			"of a proposal", member.Producers)
	}
	if member.Example.Type != "MEMBER_OF" {
		t.Errorf("the proposal carries no record to go and look at: %+v", member.Example)
	}
}

// A run that declared no vocabulary is not missing one. Producing proposals
// for it would also change the content address of every ungoverned result ever
// loaded, which is the orphaning alchemy.Fingerprint's comment declined.
//
// The way to ask "what vocabulary would this graph need" is to supply an empty
// one — a question, rather than a side effect of not asking.
func TestARunWithNoVocabularyProposesNothingAndAnEmptyOneProposesEverything(t *testing.T) {
	in := team()
	in.Vocabulary, in.OntologyID = ontology.Vocabulary{}, ""
	if rep := verify.Check(in); len(rep.Proposals) != 0 {
		t.Errorf("a run with no ontology produced %d proposals: %+v", len(rep.Proposals), rep.Proposals)
	}

	in.OntologyID = "empty@1"
	in.Vocabulary = ontology.Vocabulary{Entities: []ontology.EntityType{{Name: "Nothing"}}}
	rep := verify.Check(in)
	if len(rep.Proposals) != 4 {
		t.Fatalf("an ontology declaring nothing this corpus uses proposed %d, want 4 — Person "+
			"and Team and both relation types\n%+v", len(rep.Proposals), rep.Proposals)
	}
}
