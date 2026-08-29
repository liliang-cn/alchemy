package chunk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const topicText = `A cat sleeps sixteen hours a day.

A cat lands on its feet, usually.

A cat will ignore you on purpose.

A database keeps a write-ahead log.

A database can be sharded by key.

A database needs a backup you have restored.`

// §7.1 lists semantic as costing "an embedding pass before extraction". A nil
// Embedder is therefore a missing input, not a reason to quietly do something
// cheaper: a caller who asked for semantic and got fixed would compare two runs
// and find no trace of the substitution.
func TestSemanticWithoutAnEmbedderFails(t *testing.T) {
	got, err := Split(context.Background(), "s.txt", topicText, Options{Strategy: Semantic, MaxTokens: 100})
	if err == nil {
		t.Fatalf("want an error, got %d chunks", len(got))
	}
	if !errors.Is(err, ErrNoEmbedder) {
		t.Fatalf("want ErrNoEmbedder, got %v", err)
	}
	if got != nil {
		t.Errorf("want no chunks alongside the error, got %d", len(got))
	}
}

func TestSemanticSplitsWhereTheTopicChanges(t *testing.T) {
	got, err := Split(context.Background(), "s.txt", topicText, Options{
		Strategy:  Semantic,
		MaxTokens: 200,
		Overlap:   NoOverlap,
		Embedder:  &topicEmbedder{},
	})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 chunks, one per topic, got %d: %#v", len(got), got)
	}
	if strings.Contains(got[0].Text, "database") {
		t.Errorf("chunk 0 leaked into the second topic: %q", got[0].Text)
	}
	if strings.Contains(got[1].Text, "cat") {
		t.Errorf("chunk 1 leaked into the first topic: %q", got[1].Text)
	}
	for i, c := range got {
		if c.Strategy != string(Semantic) {
			t.Errorf("chunk %d strategy %q", i, c.Strategy)
		}
		if c.Text != topicText[c.Start:c.End] {
			t.Errorf("chunk %d offsets do not match its text", i)
		}
	}
}

func TestSemanticKeepsOneTopicTogether(t *testing.T) {
	text := "A cat sleeps.\n\nA cat lands on its feet.\n\nA cat ignores you."
	got, err := Split(context.Background(), "s.txt", text, Options{
		Strategy:  Semantic,
		MaxTokens: 200,
		Overlap:   NoOverlap,
		Embedder:  &topicEmbedder{},
	})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("blocks that resemble each other should stay together, got %d chunks", len(got))
	}
}

func TestSemanticStillObeysTheBudget(t *testing.T) {
	text := strings.Repeat("A cat sleeps a great deal of the day away.\n\n", 20)
	got, err := Split(context.Background(), "s.txt", text, Options{
		Strategy:  Semantic,
		MaxTokens: 30,
		Overlap:   NoOverlap,
		Embedder:  &topicEmbedder{},
	})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("one topic, but 20 paragraphs and a 30-token budget: got %d chunk(s)", len(got))
	}
	for i, c := range got {
		if n := approxTokens(c.Text); n > 30 {
			t.Errorf("chunk %d is %d tokens, over the budget of 30", i, n)
		}
	}
}

func TestSemanticReportsAnEmbedderFailure(t *testing.T) {
	_, err := Split(context.Background(), "s.txt", topicText, Options{
		Strategy: Semantic, MaxTokens: 100, Embedder: &topicEmbedder{err: errEmbedderDown},
	})
	if !errors.Is(err, errEmbedderDown) {
		t.Fatalf("want the embedder's own error to survive, got %v", err)
	}
}

func TestSemanticRejectsAShortVectorReply(t *testing.T) {
	_, err := Split(context.Background(), "s.txt", topicText, Options{
		Strategy: Semantic, MaxTokens: 100, Embedder: &topicEmbedder{short: true},
	})
	if err == nil {
		t.Fatal("an embedder returning fewer vectors than blocks must be an error")
	}
}
