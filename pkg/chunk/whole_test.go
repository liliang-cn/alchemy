package chunk

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestWholeReturnsOneChunkWhenItFits(t *testing.T) {
	text := "A short document. It fits."
	got, err := Split(context.Background(), "s.txt", text, Options{Strategy: Whole, MaxTokens: 100})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(got))
	}
	if got[0].Start != 0 || got[0].End != len(text) || got[0].Text != text {
		t.Errorf("chunk does not cover the document: %#v", got[0])
	}
	if got[0].Strategy != string(Whole) {
		t.Errorf("strategy %q", got[0].Strategy)
	}
}

func TestWholeFailsLoudlyWhenTheDocumentDoesNotFit(t *testing.T) {
	text := strings.Repeat("this document is far too long. ", 100)
	_, err := Split(context.Background(), "s.txt", text, Options{Strategy: Whole, MaxTokens: 50})
	if err == nil {
		t.Fatal("want an error, got nil chunks and no complaint")
	}
	var tooLarge *TooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("want a *TooLargeError, got %T: %v", err, err)
	}
	if tooLarge.MaxTokens != 50 {
		t.Errorf("MaxTokens = %d, want 50", tooLarge.MaxTokens)
	}
	if tooLarge.Tokens <= 50 {
		t.Errorf("Tokens = %d, should exceed the budget", tooLarge.Tokens)
	}
	if tooLarge.Source != "s.txt" {
		t.Errorf("Source = %q", tooLarge.Source)
	}
	// The message must name both numbers: a reader has to know by how much.
	msg := err.Error()
	if !strings.Contains(msg, strconv.Itoa(tooLarge.Tokens)) || !strings.Contains(msg, "50") {
		t.Errorf("error %q names neither the size nor the budget", msg)
	}
}
