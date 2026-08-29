package chunk

import (
	"context"
	"strings"
	"testing"
)

func TestSentencePacksWholeSentences(t *testing.T) {
	text := "Ada met Charles. They built an engine. It was never finished. History disagrees."
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Sentence, MaxTokens: 10, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("want several chunks, got %d", len(got))
	}
	for i, c := range got {
		if c.Strategy != string(Sentence) {
			t.Errorf("chunk %d strategy %q", i, c.Strategy)
		}
		// Whole sentences: a chunk ends on a terminator or on the end of text.
		if !strings.HasSuffix(c.Text, ".") {
			t.Errorf("chunk %d ends mid-sentence: %q", i, c.Text)
		}
	}
}

func TestSentenceHandlesChineseTerminators(t *testing.T) {
	text := "机器提出建议。人来决定！这样对吗？答案是肯定的。"
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Sentence, MaxTokens: 7, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 chunks, got %d: %#v", len(got), got)
	}
	if got[1].Text != "人来决定！" {
		t.Errorf("chunk 1 = %q, want %q", got[1].Text, "人来决定！")
	}
	if got[1].Start != strings.Index(text, "人来决定") {
		t.Errorf("chunk 1 Start = %d, want %d", got[1].Start, strings.Index(text, "人来决定"))
	}
}

func TestSentenceSplitsASentenceLongerThanTheBudget(t *testing.T) {
	text := strings.Repeat("word ", 200) + "end."
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Sentence, MaxTokens: 20, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	for i, c := range got {
		if n := approxTokens(c.Text); n > 20 {
			t.Errorf("chunk %d is %d tokens, over the budget of 20", i, n)
		}
	}
}
