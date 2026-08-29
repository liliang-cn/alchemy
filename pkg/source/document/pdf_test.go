package document

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPDFWithTextLayerReturnsItsText(t *testing.T) {
	res, err := Read(context.Background(), "notes.pdf",
		bytes.NewReader(textPagePDF("Alchemy reads documents.", "It never guesses.")), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(res.Text, "Alchemy reads documents.") {
		t.Errorf("Text = %q, want the first line in it", res.Text)
	}
	if !strings.Contains(res.Text, "It never guesses.") {
		t.Errorf("Text = %q, want the second line in it", res.Text)
	}
	if len(res.Unread) != 0 {
		t.Errorf("a page with text must not be reported unread: %+v", res.Unread)
	}
}
