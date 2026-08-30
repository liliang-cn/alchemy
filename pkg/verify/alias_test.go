package verify_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

func person(id, name string, p alchemy.Provenance, aliases ...string) alchemy.Entity {
	return alchemy.Entity{ID: id, Type: "Person", Name: name, Aliases: aliases, Provenance: p}
}

// The case this exists for, from a real corpus: a person says "Theodore, also
// known as Theo" and a document elsewhere calls somebody Theo. Before aliases
// were a declared field the two names could only be compared as strings, and
// "Theo" and "Theodore" share no whole word — so the strongest evidence anybody
// had was invisible to every signal.
func TestANameASourceSaidTheThingGoesByIsFoundWhereAResemblanceIsNot(t *testing.T) {
	stated := alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerHuman, By: "liliang"}
	read := alchemy.Provenance{Source: "profile.pdf", Chunk: 3, Producer: alchemy.ProducerLLMExtract}

	rep := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			person("person:theodore", "Theodore", stated, "Theo"),
			person("person:theo", "Theo", read),
		},
	})
	if len(rep.Duplicates) != 1 {
		t.Fatalf("duplicates = %d, want 1: %+v", len(rep.Duplicates), rep.Duplicates)
	}
	d := rep.Duplicates[0]
	if d.Signal != alchemy.DuplicateAlias {
		t.Errorf("signal = %q, want %q", d.Signal, alchemy.DuplicateAlias)
	}
	// The sentence has to say the evidence is somebody's words, because that
	// is the whole of what a reviewer is being told is different about it.
	for _, want := range []string{"Theo", "another name it goes by", "still a question"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("detail does not say %q:\n%s", want, d.Detail)
		}
	}
}

// It reports and never merges, and the reason is not the other signals' one.
// Theirs is that a resemblance is weak evidence. This one's is that the
// evidence is about a NAME and the question is about a NODE: two people can
// both be called Theo, and the source that named one was not talking about the
// other.
func TestAnAliasIsEvidenceAndNotAMerge(t *testing.T) {
	stated := alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerHuman}
	rep := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			person("person:theodore", "Theodore", stated, "Theo"),
			person("person:theo", "Theo", alchemy.Provenance{Source: "other.pdf", Producer: alchemy.ProducerLLMExtract}),
		},
	})
	if len(rep.Entities) != 2 {
		t.Fatalf("%d entities came back; an alias joined two nodes and nobody decided it", len(rep.Entities))
	}
}

// §5c ranks it above the resemblances, and the queue is worked top to bottom,
// so the order is the ranking. A question somebody already answered in words
// is worth a reviewer's attention before two questions about whether two
// strings look alike.
func TestAStatedAliasIsAskedBeforeAResemblance(t *testing.T) {
	stated := alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerHuman}
	read := alchemy.Provenance{Source: "profile.pdf", Chunk: 3, Producer: alchemy.ProducerLLMExtract}

	rep := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			{ID: "product:ravel", Type: "Product", Name: "Ravel", Provenance: read},
			{ID: "product:ravel gateway", Type: "Product", Name: "Ravel Gateway", Provenance: read},
			person("person:theodore", "Theodore", stated, "Theo"),
			person("person:theo", "Theo", read),
		},
	})
	if len(rep.Duplicates) < 2 {
		t.Fatalf("expected both an alias and an affix finding, got %+v", rep.Duplicates)
	}
	if rep.Duplicates[0].Signal != alchemy.DuplicateAlias {
		t.Errorf("the queue opens with %q; the stated evidence must come before the resemblances",
			rep.Duplicates[0].Signal)
	}
}

// An alias equal to the thing's own name points the lookup at itself, and an
// empty one is not a name. Neither is a question about two nodes.
func TestAnAliasThatNamesItselfOrNothingAsksNothing(t *testing.T) {
	p := alchemy.Provenance{Source: "s", Producer: alchemy.ProducerHuman}
	rep := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			person("person:theodore", "Theodore", p, "Theodore", "", "theodore"),
			person("person:other", "Somebody", p),
		},
	})
	if len(rep.Duplicates) != 0 {
		t.Fatalf("duplicates = %+v, want none", rep.Duplicates)
	}
}

// One question, not two. A pair the alias pass already asked about must not
// also arrive as a resemblance, or a reviewer sees "these look alike" beside
// "somebody said these are the same" and has to work out they are one thing.
func TestAPairTheAliasFoundIsNotAskedAgainAsAResemblance(t *testing.T) {
	stated := alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerHuman}
	read := alchemy.Provenance{Source: "profile.pdf", Chunk: 3, Producer: alchemy.ProducerLLMExtract}

	rep := verify.Check(verify.Input{
		Entities: []alchemy.Entity{
			person("person:theodore", "Theodore", stated, "Theodore Okonkwo"),
			person("person:theodore okonkwo", "Theodore Okonkwo", read),
		},
	})
	if len(rep.Duplicates) != 1 {
		t.Fatalf("%d findings for one pair: %+v", len(rep.Duplicates), rep.Duplicates)
	}
	if rep.Duplicates[0].Signal != alchemy.DuplicateAlias {
		t.Errorf("the pair came back as %q; the stated evidence must win", rep.Duplicates[0].Signal)
	}
}
