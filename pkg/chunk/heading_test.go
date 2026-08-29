package chunk

import (
	"context"
	"strings"
	"testing"
)

const manual = `Preamble text with no heading of its own.

# Installation

Run the installer. It asks two questions.

## Requirements

A machine and some patience.

# Usage

Type the command.
`

func TestHeadingMakesASectionAChunk(t *testing.T) {
	got, err := Split(context.Background(), "m.md", manual, Options{Strategy: Heading, MaxTokens: 100, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	want := []string{"", "Installation", "Requirements", "Usage"}
	if len(got) != len(want) {
		t.Fatalf("want %d chunks, got %d: %#v", len(want), len(got), got)
	}
	for i, c := range got {
		if c.Heading != want[i] {
			t.Errorf("chunk %d Heading = %q, want %q", i, c.Heading, want[i])
		}
		if c.Strategy != string(Heading) {
			t.Errorf("chunk %d Strategy = %q", i, c.Strategy)
		}
		if c.Text != manual[c.Start:c.End] {
			t.Errorf("chunk %d offsets do not match its text", i)
		}
	}
	// The heading line itself belongs to the section: it is the context an
	// extractor reads the section under.
	if !strings.HasPrefix(got[1].Text, "# Installation") {
		t.Errorf("chunk 1 dropped its heading line: %q", got[1].Text)
	}
}

func TestHeadingRecognisesHTML(t *testing.T) {
	html := "<h1>Chapter One</h1>\n<p>Some prose.</p>\n<h2 class=\"sub\">Section Two</h2>\n<p>More prose.</p>\n"
	got, err := Split(context.Background(), "m.html", html, Options{Strategy: Heading, MaxTokens: 100, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 chunks, got %d: %#v", len(got), got)
	}
	if got[0].Heading != "Chapter One" || got[1].Heading != "Section Two" {
		t.Errorf("headings = %q, %q", got[0].Heading, got[1].Heading)
	}
}

// §7.1 names this as the cost of the heading strategy: a long section exceeds
// any context. It must be split, and the chunk must not pretend it was not.
func TestHeadingSplitsAnOversizedSectionAndSaysSo(t *testing.T) {
	long := "# 长章节\n\n" + strings.Repeat("这一节很长，长到没有任何模型能一次读完。", 30)
	got, err := Split(context.Background(), "m.md", long, Options{Strategy: Heading, MaxTokens: 40, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("oversized section came back in %d chunk(s)", len(got))
	}
	for i, c := range got {
		if n := approxTokens(c.Text); n > 40 {
			t.Errorf("chunk %d is %d tokens, over the budget of 40", i, n)
		}
		if c.Heading != "长章节" {
			t.Errorf("chunk %d lost its heading: %q", i, c.Heading)
		}
		if c.Strategy == string(Heading) {
			t.Errorf("chunk %d claims plain %q though the section had to be cut", i, Heading)
		}
		if c.Strategy != HeadingSplit {
			t.Errorf("chunk %d Strategy = %q, want %q", i, c.Strategy, HeadingSplit)
		}
	}
}

func TestHeadingIgnoresAHashThatIsNotAHeading(t *testing.T) {
	text := "# Real heading\n\nA line about issue #42 and a C directive.\n\n#not-a-heading either\n"
	got, err := Split(context.Background(), "m.md", text, Options{Strategy: Heading, MaxTokens: 100, Overlap: NoOverlap})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 chunk, got %d: %#v", len(got), got)
	}
}
