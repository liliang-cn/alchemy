package chunk

import (
	"context"
	"strings"
	"testing"
)

// §7.1: the default is heading, falling back to paragraph then fixed. What the
// chunks must never say is "auto" — that tells a reader comparing two runs
// nothing about what actually happened.
func TestAutoNamesTheStrategyThatRan(t *testing.T) {
	oneBlob := strings.Repeat("a single unbroken run of words with no structure at all ", 60)
	cases := []struct {
		name string
		text string
		want string
	}{
		{"headings win", manual, string(Heading)},
		{"paragraphs when there are no headings", "One paragraph here.\n\nAnother paragraph here.", string(Paragraph)},
		{"fixed when there is no structure", oneBlob, string(Fixed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Split(context.Background(), "s.txt", tc.text, Options{MaxTokens: 40})
			if err != nil {
				t.Fatalf("Split: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("no chunks")
			}
			for i, c := range got {
				if c.Strategy == string(Auto) {
					t.Fatalf("chunk %d says %q", i, Auto)
				}
				if c.Strategy != tc.want && !strings.HasPrefix(c.Strategy, tc.want+"+") {
					t.Errorf("chunk %d Strategy = %q, want %q", i, c.Strategy, tc.want)
				}
			}
		})
	}
}

func TestAutoIsTheZeroValue(t *testing.T) {
	opts, err := Options{}.normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if opts.Strategy != Auto {
		t.Errorf("zero Strategy normalised to %q, want %q", opts.Strategy, Auto)
	}
	if opts.MaxTokens != DefaultMaxTokens {
		t.Errorf("zero MaxTokens normalised to %d", opts.MaxTokens)
	}
}

func TestExplicitHeadingOnAHeadinglessDocumentSaysWhatRan(t *testing.T) {
	text := "One paragraph here.\n\nAnother paragraph here."
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Heading, MaxTokens: 5, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no chunks")
	}
	for i, c := range got {
		if c.Strategy != string(Paragraph) {
			t.Errorf("chunk %d Strategy = %q, want %q — the document had no headings", i, c.Strategy, Paragraph)
		}
	}
}

// A tenth of a small budget rounds to nothing, and a default that silently
// becomes zero is exactly the insurance §7.1 says must be on by default.
func TestDefaultOverlapSurvivesASmallBudget(t *testing.T) {
	for _, budget := range []int{2, 5, 9, 10, 1000} {
		opts, err := Options{MaxTokens: budget}.normalise()
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		if opts.Overlap <= 0 {
			t.Errorf("budget %d: default Overlap is %d", budget, opts.Overlap)
		}
		if opts.Overlap >= budget {
			t.Errorf("budget %d: default Overlap %d leaves no room", budget, opts.Overlap)
		}
	}
}
