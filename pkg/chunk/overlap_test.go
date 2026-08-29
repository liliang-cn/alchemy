package chunk

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The fact whose two ends a boundary would separate. §7.1: a relation cut in
// half by a boundary is recovered when the next chunk starts before the
// previous one ended.
const splitRelation = "Ada Lovelace worked with Charles Babbage."

func relationText() string {
	// Padding sized so the default budget lands the boundary inside the
	// relation rather than politely between sentences.
	pad := strings.Repeat("filler words that carry no relation at all. ", 4)
	return pad + splitRelation + " " + pad
}

func TestOverlapRecoversARelationTheBoundaryCut(t *testing.T) {
	text := relationText()

	without, err := Split(context.Background(), "s.txt", text, Options{Strategy: Fixed, MaxTokens: 25, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split without overlap: %v", err)
	}
	if containsWhole(without, splitRelation) {
		t.Fatalf("test is not exercising the split-relation problem: some chunk already holds the whole relation")
	}

	with, err := Split(context.Background(), "s.txt", text, Options{Strategy: Fixed, MaxTokens: 25})
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
