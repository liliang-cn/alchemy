package document

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/chunk"
)

// pkg/chunk's heading strategy finds sections by scanning for "#" at the start
// of a line, so a markdown reader that reflows or re-indents anything destroys
// the only structure the chunker has.
func TestMarkdownPassesThroughWithHeadingsIntact(t *testing.T) {
	const src = "# Title\n\nA paragraph.\n\n## Section two\n\n- a list item\n- another\n\n```go\nfunc main() {}\n```\n"
	res, err := Read(context.Background(), "notes.md", strings.NewReader(src), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Text != src {
		t.Errorf("markdown was altered:\n got %q\nwant %q", res.Text, src)
	}
	if len(res.Unread) != 0 {
		t.Errorf("markdown produced Unread: %+v", res.Unread)
	}
}

func TestPlainTextPassesThroughUnchanged(t *testing.T) {
	const src = "line one\n\nline two\n\ttabbed\n"
	res, err := Read(context.Background(), "readme.txt", strings.NewReader(src), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Text != src {
		t.Errorf("plain text was altered:\n got %q\nwant %q", res.Text, src)
	}
}

// The passthrough requirement exists for one reason, so it is worth proving
// against the thing that depends on it rather than against a string compare.
func TestMarkdownHeadingsStillReachTheChunker(t *testing.T) {
	const src = "# Alchemy\n\nIntro prose.\n\n## Sources\n\nddl, tabular, document, graph.\n\n### Document\n\nPDF, markdown, HTML.\n"
	res, err := Read(context.Background(), "design.md", strings.NewReader(src), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	chunks, err := chunk.Split(context.Background(), "design.md", res.Text, chunk.Options{
		Strategy: chunk.Heading, MaxTokens: 200, Overlap: chunk.NoOverlap,
	})
	if err != nil {
		t.Fatalf("chunk.Split: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range chunks {
		seen[c.Heading] = true
	}
	for _, want := range []string{"Alchemy", "Sources", "Document"} {
		if !seen[want] {
			t.Errorf("pkg/chunk did not see heading %q; it saw %v", want, seen)
		}
	}
}
