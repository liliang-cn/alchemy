package chunk

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmptyTextIsNoChunksAndNoComplaint(t *testing.T) {
	for _, s := range []Strategy{Fixed, Sentence, Paragraph, Heading, Whole, Auto} {
		got, err := Split(context.Background(), "s.txt", "   \n\n  ", Options{Strategy: s, MaxTokens: 50})
		if err != nil {
			t.Errorf("%s: %v", s, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: want no chunks from whitespace, got %d: %#v", s, len(got), got)
		}
	}
}

func TestUnknownStrategyIsRejected(t *testing.T) {
	_, err := Split(context.Background(), "s.txt", "text", Options{Strategy: "vibes"})
	if err == nil {
		t.Fatal("want an error for an unknown strategy")
	}
	if !strings.Contains(err.Error(), "vibes") {
		t.Errorf("error %q does not name the strategy", err)
	}
}

func TestOverlapAtOrAboveTheBudgetIsRejected(t *testing.T) {
	_, err := Split(context.Background(), "s.txt", "text", Options{Strategy: Fixed, MaxTokens: 10, Overlap: 10})
	if err == nil {
		t.Fatal("an overlap that cannot make progress must be refused, not clamped")
	}
}

// The rule every strategy owes a reader: the chunk is findable in the original.
func TestOffsetsAreExactForEveryStrategy(t *testing.T) {
	// Mixed scripts, headings, blank lines and a long unbroken run, so every
	// code path that computes an offset is exercised on multi-byte text.
	text := "# 第一章 提出与决定\n\n机器提出建议，由人来决定。每一条边都带着它的来源。\n\n" +
		"## Second section\n\nA paragraph in English. And another sentence.\n\n" +
		strings.Repeat("很长的一段没有空行的中文文字，用来触发按预算切分的路径。", 8)
	for _, s := range []Strategy{Fixed, Sentence, Paragraph, Heading, Semantic, Auto} {
		opts := Options{Strategy: s, MaxTokens: 40}
		if s == Semantic {
			opts.Embedder = &topicEmbedder{}
		}
		got, err := Split(context.Background(), "mixed.md", text, opts)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no chunks", s)
		}
		for i, c := range got {
			if c.Start < 0 || c.End > len(text) || c.Start >= c.End {
				t.Fatalf("%s chunk %d: offsets %d:%d outside 0:%d", s, i, c.Start, c.End, len(text))
			}
			if c.Text != text[c.Start:c.End] {
				t.Errorf("%s chunk %d: Text != text[%d:%d]", s, i, c.Start, c.End)
			}
			if !utf8.ValidString(c.Text) {
				t.Errorf("%s chunk %d: cut a rune in half", s, i)
			}
			if c.Index != i {
				t.Errorf("%s chunk %d: Index = %d", s, i, c.Index)
			}
			if c.Source != "mixed.md" {
				t.Errorf("%s chunk %d: Source = %q", s, i, c.Source)
			}
			if c.Strategy == "" {
				t.Errorf("%s chunk %d: no strategy recorded", s, i)
			}
			if n := approxTokens(c.Text); n > opts.MaxTokens {
				t.Errorf("%s chunk %d: %d tokens, over the budget of %d", s, i, n, opts.MaxTokens)
			}
		}
		// Chunks move forward, and each one starts inside or at its predecessor.
		for i := 1; i < len(got); i++ {
			if got[i].Start <= got[i-1].Start || got[i].End <= got[i-1].End {
				t.Errorf("%s chunk %d does not advance: %d:%d after %d:%d", s, i, got[i].Start, got[i].End, got[i-1].Start, got[i-1].End)
			}
		}
	}
}

func TestApproxTokensCountsCJKMoreHeavilyThanASCII(t *testing.T) {
	if approxTokens("abcd") != 1 {
		t.Errorf("four ASCII runes = %d tokens, want 1", approxTokens("abcd"))
	}
	if approxTokens("中文") != 2 {
		t.Errorf("two CJK runes = %d tokens, want 2", approxTokens("中文"))
	}
	if approxTokens("") != 0 {
		t.Errorf("empty string = %d tokens", approxTokens(""))
	}
}
