package contributions

import (
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// The three rules that make two reads of one node the same document, asserted
// without a server, because they are the part that is not a query.
//
// Every connector calls this with the mentions its own store could find, in
// whatever order its own query produced; if the folding differed between them a
// buyer comparing two backends would be comparing shuffles and no test against
// a single store would say so.
func TestOneSourceMentioningANodeSeveralTimesIsOneContributor(t *testing.T) {
	pdf := alchemy.ProducerLLMExtract
	got := Assemble("person:mira", "Person", []recall.Contributor{
		// The node's own record, which is the one that carries a name.
		{Source: "halcyon-profile.pdf", Chunk: 20, Producer: pdf, Name: "Mira"},
		// Two edges the same sentence produced. They are two claims and one
		// mention: a reader counting them would read one sentence as though two
		// documents had agreed.
		{Source: "halcyon-profile.pdf", Chunk: 20, Producer: pdf},
		{Source: "halcyon-profile.pdf", Chunk: 20, Producer: pdf},
		{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport},
	})
	want := recall.Contributions{
		ID: "person:mira", Type: "Person",
		Contributors: []recall.Contributor{
			{Source: "halcyon-profile.pdf", Chunk: 20, Producer: pdf, Stated: false, Name: "Mira"},
			{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Stated: true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Assemble =\n%+v\nwant\n%+v", got, want)
	}
	if !got.Joined() {
		t.Error("Joined() = false where two files had a hand in the node")
	}
}

// A different chunk of one file is a different mention. Chunk boundaries decide
// what an extractor could see, so two chunks of one PDF naming a node are two
// occasions on which somebody read it — which is the corroboration a reader is
// weighing, even though Joined() will still say one source.
func TestTwoChunksOfOneFileAreTwoMentionsAndStillOneSource(t *testing.T) {
	got := Assemble("e1", "System", []recall.Contributor{
		{Source: "a.pdf", Chunk: 9, Producer: alchemy.ProducerLLMExtract},
		{Source: "a.pdf", Chunk: 2, Producer: alchemy.ProducerLLMExtract, Name: "Ravel"},
	})
	if len(got.Contributors) != 2 {
		t.Fatalf("Contributors = %+v, want two mentions", got.Contributors)
	}
	// Ordered by source then chunk, so the same node read twice reads the same.
	if got.Contributors[0].Chunk != 2 || got.Contributors[1].Chunk != 9 {
		t.Errorf("Contributors are not in chunk order: %+v", got.Contributors)
	}
	if got.Joined() {
		t.Error("Joined() = true where one file mentioned the node twice; " +
			"a source repeating itself is not two sources agreeing")
	}
}

// One file read by two producers is two mentions, and without the producer in
// the sort key the order between them is whatever the store returned.
func TestOneFileReadByTwoProducersHasAnOrderAtAll(t *testing.T) {
	first := Assemble("e1", "System", []recall.Contributor{
		{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL},
		{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerLLMExtract},
	})
	second := Assemble("e1", "System", []recall.Contributor{
		{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerLLMExtract},
		{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL},
	})
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two orderings of one node's mentions produced two documents:\n%+v\n%+v", first, second)
	}
}

// Stated is alchemy.Producer.Deterministic and is computed here rather than
// carried in, for the reason recall.NewClaim gives about the same field: a
// stored boolean is the answer the rule gave on the day of the import, and a
// reader deciding today how far to trust a source should be told today's
// answer.
func TestStatedIsRecomputedFromTheProducerAndNotTakenFromTheCaller(t *testing.T) {
	got := Assemble("e1", "System", []recall.Contributor{
		// A caller passing the opposite of the truth, which is what a store
		// reading its own materialised column would do a year after the rule
		// changed.
		{Source: "a.pdf", Chunk: 1, Producer: alchemy.ProducerLLMExtract, Stated: true},
		{Source: "b.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Stated: false},
	})
	if got.Contributors[0].Stated {
		t.Error("a model's extraction came back stated")
	}
	if !got.Contributors[1].Stated {
		t.Error("a graph import came back inferred")
	}
}

// A source that referred to a node without this store recording what it called
// it contributes no name, and an empty string is not a name.
//
// A reader counting Names is asking "did the records agree about what this is
// called". An empty string in the list would answer that question with a name
// nobody used, and a node all of whose contributions came from edges would come
// back claiming one.
func TestASourceWithNoRecoverableNameContributesNoName(t *testing.T) {
	got := Assemble("person:mira", "Person", []recall.Contributor{
		{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport},
		{Source: "docs.md", Chunk: 3, Producer: alchemy.ProducerLLMExtract},
	})
	for _, c := range got.Contributors {
		if c.Name != "" {
			t.Errorf("contributor %+v carries a name; nothing here records what either file called the node", c)
		}
	}
}

// Nothing mentioned it is a zero Contributions and not an empty one, because
// that is the value the interface specifies for an id the load does not hold
// and a caller comparing against recall.Contributions{} must not have to know
// whether a slice came back nil or empty.
func TestNothingMentionedItIsAZeroContributions(t *testing.T) {
	if got := Assemble("e1", "System", nil); !reflect.DeepEqual(got, recall.Contributions{}) {
		t.Errorf("Assemble with no mentions = %+v, want a zero Contributions", got)
	}
}
