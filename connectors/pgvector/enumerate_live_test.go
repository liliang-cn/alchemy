package pgvector

import (
	"context"
	"strings"
	"testing"
)

// The two questions an agent could not ask, and the cross-check between them.
//
// Measured before this existed: asked what kinds of thing a graph held, an
// agent made eighty-three tool calls -- an anchor search per letter of the
// alphabet -- and produced a table right about the total and wrong in five
// places under it: thirteen types where the load has fourteen, four counts off,
// and one row reading "1-2" because it could not tell. Asked to list every
// person it named thirteen of twenty-one, having said twenty in that table a
// minute before. One graph, two runs, two answers that disagree with each
// other, neither hedged.
//
// So the test is not only that each answers: it is that they answer the SAME
// number. A vocabulary whose counts do not match what reading the class out
// returns is the defect above with a tool in front of it.
func TestTheVocabularyCanBeReadAndAgreesWithReadingEachClassOut(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	const load = "recall-types"
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122), LoadOptions{ID: load}); err != nil {
		t.Fatalf("load: %v", err)
	}

	types, err := l.Types(ctx, load)
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	if len(types) == 0 {
		t.Fatal("no types in a load that holds entities")
	}
	// Ordered by type, because a vocabulary read twice must come out the same
	// or a diff between two runs is comparing shuffles.
	for i := 1; i < len(types); i++ {
		if types[i-1].Type >= types[i].Type {
			t.Fatalf("types are not ordered: %v", types)
		}
	}

	total := 0
	for _, tc := range types {
		if tc.Count <= 0 {
			t.Errorf("type %q reports a count of %d; a type with no entities is not in the vocabulary",
				tc.Type, tc.Count)
		}
		total += tc.Count

		// The count is what tells a caller which limit to pass, so passing it
		// has to return exactly that many and say so.
		got, err := l.OfType(ctx, load, tc.Type, tc.Count)
		if err != nil {
			t.Fatalf("OfType(%q): %v", tc.Type, err)
		}
		if got.Total != tc.Count || len(got.Nodes) != tc.Count {
			t.Errorf("OfType(%q) returned %d of %d, Types said %d; the count is what a caller sizes "+
				"the read by, and two numbers that disagree make one of them a guess",
				tc.Type, len(got.Nodes), got.Total, tc.Count)
		}
		if got.Truncated() {
			t.Errorf("OfType(%q) with the limit Types gave still reports a page", tc.Type)
		}
		for _, n := range got.Nodes {
			if n.Type != tc.Type {
				t.Errorf("OfType(%q) returned a %s; the type is matched exactly", tc.Type, n.Type)
			}
		}
	}

	// Everything the load holds is in exactly one class, so the vocabulary adds
	// up to the graph. A type quietly dropped would leave every count right and
	// the total wrong, which is the shape of the answer this replaces.
	all, err := l.Find(ctx, load, "", total+1)
	if err != nil {
		t.Fatalf("Find(\"\"): %v", err)
	}
	if all.Total != total {
		t.Errorf("the types sum to %d and the load holds %d entities", total, all.Total)
	}

	// A type the load does not have is an empty page and not an error, and not
	// the entities of some other class either -- which is what a case fold or a
	// substring match here would return.
	for _, absent := range []string{"NoSuchType", strings.ToLower(types[0].Type), types[0].Type + "s"} {
		if absent == types[0].Type {
			continue
		}
		got, err := l.OfType(ctx, load, absent, 10)
		if err != nil {
			t.Fatalf("OfType(%q): %v", absent, err)
		}
		if len(got.Nodes) != 0 || got.Total != 0 {
			t.Errorf("OfType(%q) returned %d entities; a type is declared by an ontology "+
				"and is not matched loosely", absent, len(got.Nodes))
		}
	}

	// limit <= 0 is refused rather than meaning everything, as with Find.
	if _, err := l.OfType(ctx, load, types[0].Type, 0); err == nil {
		t.Error("OfType with limit 0 succeeded; there is no everything value")
	}
}
