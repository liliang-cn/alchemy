package neo4j

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// contributed is the graph Contributions exists for, reduced to its bones.
//
// It is the Halcyon corpus's Mira as the live store actually holds him, run
// 710fd205: one node whose own record came from a PDF, carrying one edge
// asserted by that PDF and one asserted by a hand-written team file. The node
// admits one source and two contributed, and until this method existed nothing
// in the read interface could say so — Find, Claims, Cite and Unanswered all
// report the node as though one document had described it.
//
// org:halcyon and product:ledger are the control, and they are why this fixture
// has three entities rather than one. Each is named by one source and touched
// only by an edge from that same source, so "two documents agreed" and "one
// document said it twice" have to come back differently. A method that could
// not tell them apart would mark the whole graph as joined, which is the same
// unusable answer as marking none of it.
func contributed() alchemy.Result {
	pdf := alchemy.Provenance{
		Source: "halcyon-profile.pdf", Chunk: 20, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading", Confidence: 0.81,
	}
	team := alchemy.Provenance{
		Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Ontology: "sds@3",
	}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "person:mira", Type: "Person", Name: "Mira", Provenance: pdf},
			{ID: "org:halcyon", Type: "Company", Name: "Halcyon", Provenance: pdf},
			{ID: "product:ledger", Type: "Product", Name: "Ledger", Provenance: team},
		},
		Relations: []alchemy.Relation{
			{From: "person:mira", To: "org:halcyon", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: pdf},
			{From: "person:mira", To: "product:ledger", Type: "DEVELOPS", Provenance: team},
		},
		Chunks: []alchemy.Chunk{{
			Index: 20, Text: "Mira is the CTO of Halcyon.", Source: "halcyon-profile.pdf",
			Strategy: "heading", Heading: "Team", Start: 400, End: 426,
		}},
	}
}

// contributing loads the fixture and returns the loader and the load's name.
func contributing(t *testing.T) (*Loader, string) {
	t.Helper()
	l := liveLoader(t, Options{RunID: "contrib"})
	if _, err := l.Load(context.Background(), contributed()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return l, l.opts.RunID
}

// The claim the whole primitive makes: a join the loader PERFORMED is visible,
// and one source repeating itself is not mistaken for two agreeing.
//
// Unanswered reports the joins the loader refused. Nothing reported the ones it
// made, so an agent could be careful only about the half the machine was
// already unsure of — which is the wrong half, because the other one has been
// acted on. Six runs of six over this graph said "Mira is the CTO" as though
// two mentions of a bare first name were established to be one person.
func TestAJoinTheLoaderMadeIsVisibleAndOneSourceSayingItTwiceIsNot(t *testing.T) {
	l, load := contributing(t)
	ctx := context.Background()

	got, err := l.Contributions(ctx, load, "person:mira")
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	if got.ID != "person:mira" || got.Type != "Person" {
		t.Errorf("Contributions = %+v, want the node it was asked about echoed back", got)
	}
	want := []recall.Contributor{
		{
			Source: "halcyon-profile.pdf", Chunk: 20, Producer: alchemy.ProducerLLMExtract,
			Stated: false, Name: "Mira",
		},
		{
			Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport,
			Stated: true, Name: "",
		},
	}
	if !reflect.DeepEqual(got.Contributors, want) {
		t.Errorf("Contributors =\n%+v\nwant\n%+v", got.Contributors, want)
	}
	if !got.Joined() {
		t.Error("Joined() = false on a node two documents had a hand in")
	}

	// The control. One document named it and the same document asserted the one
	// edge that touches it, so there is one mention and no join to report.
	only, err := l.Contributions(ctx, load, "org:halcyon")
	if err != nil {
		t.Fatalf("Contributions(org:halcyon): %v", err)
	}
	if len(only.Contributors) != 1 || only.Contributors[0].Source != "halcyon-profile.pdf" {
		t.Fatalf("Contributions(org:halcyon).Contributors = %+v, want the one source that said it", only.Contributors)
	}
	if only.Joined() {
		t.Error("Joined() = true on a node one document named and one document referred to; " +
			"a source repeating itself is not two sources agreeing")
	}
}

// Name is what THAT source called the node, and a source that only referred to
// the node by ID did not call it anything.
//
// This is the assertion that keeps the primitive honest. The node's own name is
// on the node, so it is trivial to put it on every contributor, and doing so
// would make every join look unanimous — "all three sources said Nadia
// Okonkwo" — which is exactly the false confidence this method exists to end.
// team.json asserted an edge whose endpoint is the ID `person:mira`; no store
// here records what that file called him, and an empty Name says so.
func TestASourceThatOnlyReferredToANodeIsNotRecordedAsHavingNamedIt(t *testing.T) {
	l, load := contributing(t)
	got, err := l.Contributions(context.Background(), load, "person:mira")
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	for _, c := range got.Contributors {
		if c.Source == "team.json" && c.Name != "" {
			t.Errorf("the team.json contributor is recorded as calling the node %q; "+
				"an edge names its endpoints by ID and this store keeps no name for them, "+
				"so repeating the node's own name here would invent unanimity", c.Name)
		}
	}
	if want := []string{"Mira"}; !reflect.DeepEqual(got.Names, want) {
		t.Errorf("Names = %v, want %v: only the record that named the node contributes a name", got.Names, want)
	}
}

// A pack built twice from one unchanged load must come out the same, or an
// agent's cache, a diff between two runs and a person re-reading yesterday's
// answer are all comparing shuffles.
func TestTwoReadsOfOneNodeProduceTheSameDocument(t *testing.T) {
	l, load := contributing(t)
	ctx := context.Background()
	first, err := l.Contributions(ctx, load, "person:mira")
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	second, err := l.Contributions(ctx, load, "person:mira")
	if err != nil {
		t.Fatalf("Contributions again: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two reads of one node differ:\n%+v\n%+v", first, second)
	}
}

// The asymmetry the interface states. A load that is not there is a caller
// naming the wrong import — the bug the load parameter exists for, arriving as
// a typo — and an id that is not there is an ordinary answer to "what
// contributed to this": nothing.
func TestAnUnknownIdContributedNothingAndAnUnknownLoadIsAMistake(t *testing.T) {
	l, load := contributing(t)
	ctx := context.Background()

	got, err := l.Contributions(ctx, load, "person:nobody")
	if err != nil {
		t.Errorf("Contributions of an absent id = %v, want no error", err)
	}
	if !reflect.DeepEqual(got, recall.Contributions{}) {
		t.Errorf("Contributions of an absent id = %+v, want a zero Contributions", got)
	}

	if _, err := l.Contributions(ctx, "contrib-typo", "person:mira"); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Contributions in an unknown load = %v, want ErrNoLoad", err)
	}
}
