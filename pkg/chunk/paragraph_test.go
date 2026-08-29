package chunk

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParagraphSplitsOnBlankLines(t *testing.T) {
	text := "First paragraph, one line.\n\nSecond paragraph, also one line.\n\nThird."
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Paragraph, MaxTokens: 8, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 chunks, got %d: %#v", len(got), got)
	}
	for i, c := range got {
		if c.Strategy != string(Paragraph) {
			t.Errorf("chunk %d strategy %q", i, c.Strategy)
		}
		if strings.Contains(c.Text, "\n\n") {
			t.Errorf("chunk %d spans a blank line: %q", i, c.Text)
		}
	}
	if !strings.HasPrefix(got[1].Text, "Second") {
		t.Errorf("chunk 1 = %q", got[1].Text)
	}
}

func TestParagraphPacksSmallParagraphsToTheBudget(t *testing.T) {
	text := "a b.\n\nc d.\n\ne f.\n\ng h."
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Paragraph, MaxTokens: 100, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("four tiny paragraphs fit one budget, want 1 chunk, got %d", len(got))
	}
}

// Offsets are byte offsets into the source, and Chinese text is three bytes a
// rune, so an off-by-one between runes and bytes cannot hide here.
func TestParagraphOffsetsAreBytesNotRunes(t *testing.T) {
	text := "机器提出建议，由人来决定。\n\n每一条边都带着它的来源。\n\n冲突永远需要一个人来裁决。"
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Paragraph, MaxTokens: 20, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(got))
	}
	if got[1].Start != strings.Index(text, "每一条边") {
		t.Errorf("chunk 1 Start = %d, want byte offset %d", got[1].Start, strings.Index(text, "每一条边"))
	}
	for i, c := range got {
		if c.Text != text[c.Start:c.End] {
			t.Errorf("chunk %d: Text %q != text[%d:%d] = %q", i, c.Text, c.Start, c.End, text[c.Start:c.End])
		}
		if !utf8.ValidString(c.Text) {
			t.Errorf("chunk %d cut a rune in half: %q", i, c.Text)
		}
	}
}

// A single paragraph larger than the budget must not come back oversized.
func TestParagraphSplitsAnOversizedParagraph(t *testing.T) {
	long := strings.Repeat("这是一个很长的段落，没有任何空行可以用来切分。", 20)
	got, err := Split(context.Background(), "s.txt", long, Options{Strategy: Paragraph, MaxTokens: 30, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("oversized paragraph came back in %d chunk(s)", len(got))
	}
	for i, c := range got {
		if n := approxTokens(c.Text); n > 30 {
			t.Errorf("chunk %d is %d tokens, over the budget of 30", i, n)
		}
		if !utf8.ValidString(c.Text) {
			t.Errorf("chunk %d cut a rune in half", i)
		}
	}
}
