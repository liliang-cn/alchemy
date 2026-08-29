package chunk

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The fact whose two ends a boundary would separate. §7.1: a relation cut in
// half by a boundary is recovered when the next chunk starts before the
// previous one ended.
const splitRelation = "Ada Lovelace worked with Charles Babbage."

// relationBudget is chosen so that the default overlap (a tenth of it) is
// wider than the relation: overlap only insures against boundaries that cut
// something shorter than the overlap itself, which is the honest limit of the
// insurance and the reason the budget here is a realistic one.
const relationBudget = 200

func relationText() string {
	pad := "filler words that carry no relation at all. "
	var b strings.Builder
	// Pad up to just under the first boundary (relationBudget tokens of ASCII
	// is four times as many bytes), so the relation straddles it.
	for b.Len() < relationBudget*4-10 {
		b.WriteString(pad)
	}
	b.WriteString(splitRelation)
	b.WriteString(" ")
	b.WriteString(pad)
	return b.String()
}

func TestOverlapRecoversARelationTheBoundaryCut(t *testing.T) {
	text := relationText()

	without, err := Split(context.Background(), "s.txt", text, Options{Strategy: Fixed, MaxTokens: relationBudget, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split without overlap: %v", err)
	}
	if containsWhole(without, splitRelation) {
		t.Fatalf("test is not exercising the split-relation problem: some chunk already holds the whole relation")
	}

	with, err := Split(context.Background(), "s.txt", text, Options{Strategy: Fixed, MaxTokens: relationBudget})
	if err != nil {
		t.Fatalf("Split with default overlap: %v", err)
	}
	if !containsWhole(with, splitRelation) {
		t.Errorf("no chunk recovered %q; overlap did not do its job", splitRelation)
	}
}

func TestOverlapIsNonZeroByDefault(t *testing.T) {
	opts, err := Options{MaxTokens: 100}.normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if opts.Overlap <= 0 {
		t.Errorf("default Overlap is %d, want non-zero", opts.Overlap)
	}
}

// containsWhole reports whether any single chunk holds s entire.
func containsWhole(chunks []alchemy.Chunk, s string) bool {
	for _, c := range chunks {
		if strings.Contains(c.Text, s) {
			return true
		}
	}
	return false
}

// Overlap is not a property of the fixed baseline alone: §7.1 states it as one
// of "two rules that hold whichever is chosen".
func TestOverlapAppliesToTheStructuralStrategies(t *testing.T) {
	text := "Alpha paragraph about a thing.\n\nBeta paragraph about another.\n\nGamma paragraph about a third.\n\nDelta paragraph, the last."
	for _, s := range []Strategy{Paragraph, Sentence} {
		got, err := Split(context.Background(), "s.txt", text, Options{Strategy: s, MaxTokens: 10})
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if len(got) < 2 {
			t.Fatalf("%s: want several chunks, got %d", s, len(got))
		}
		overlapped := 0
		for i := 1; i < len(got); i++ {
			if got[i].Start < got[i-1].End {
				overlapped++
			}
		}
		if overlapped == 0 {
			t.Errorf("%s: no chunk starts before its predecessor ended", s)
		}
	}
}

func TestCancelledContextStopsBeforeAnyWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Split(ctx, "s.txt", manual, Options{MaxTokens: 50})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
