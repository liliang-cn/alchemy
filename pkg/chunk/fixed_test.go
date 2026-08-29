package chunk

import (
	"context"
	"testing"
)

func TestFixedSplitsIntoSeveralChunksUnderBudget(t *testing.T) {
	text := ""
	for i := 0; i < 40; i++ {
		text += "the quick brown fox jumps over the lazy dog. "
	}
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Fixed, MaxTokens: 20})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("want several chunks, got %d", len(got))
	}
	for i, c := range got {
		if c.Index != i {
			t.Errorf("chunk %d has Index %d", i, c.Index)
		}
		if c.Source != "s.txt" {
			t.Errorf("chunk %d has Source %q", i, c.Source)
		}
		if c.Strategy != string(Fixed) {
			t.Errorf("chunk %d has Strategy %q, want %q", i, c.Strategy, Fixed)
		}
		if c.Text != text[c.Start:c.End] {
			t.Errorf("chunk %d text does not match its offsets", i)
		}
	}
}
