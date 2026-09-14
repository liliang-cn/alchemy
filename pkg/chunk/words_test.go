package chunk

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// No chunk begins or ends inside a word, under any strategy.
//
// This is not tidiness. Extraction is one call per chunk and the model reads
// what it is handed, so a chunk beginning "ph is responsible for
// LINSTOR-Gateway" is a chunk in which somebody named ph is responsible for
// LINSTOR-Gateway. That happened on a real load: the overlap retreated four
// characters into "Christoph", the next chunk started at "ph", and a Person
// named "ph" went into the graph wired to a real product — citing a chunk that
// really does say it, violating no rule, and not a duplicate of "christoph" by
// any signal duplicates.go trusts, because a bare suffix is the "Ada"/"Nevada"
// shape that signal refuses on purpose.
//
// Nothing downstream can catch this. It has to not happen.
func TestNoChunkBeginsOrEndsInsideAWord(t *testing.T) {
	text := "# Who is responsible for what\n\n" +
		"Everyone in this section works at LINBIT.\n\n" +
		"Christoph is responsible for LINSTOR-Gateway. Christoph is responsible for\n" +
		"LINBIT VSAN.\n\n" +
		"Rene is responsible for CloudStack HCI. Moritz is responsible for CloudStack\n" +
		"HCI. Christoph is responsible for CloudStack HCI.\n\n" +
		"Roland is responsible for DRBD Reactor.\n\n" +
		"Moritz is responsible for Kubernetes CSI driver.\n"

	for _, s := range []Strategy{Auto, Heading, Paragraph, Sentence, Fixed} {
		for _, budget := range []int{16, 24, 40, 64} {
			got, err := Split(context.Background(), "people.md", text, Options{Strategy: s, MaxTokens: budget})
			if err != nil {
				t.Fatalf("%s at %d tokens: %v", s, budget, err)
			}
			for i, c := range got {
				if c.Text != text[c.Start:c.End] {
					t.Fatalf("%s at %d tokens: chunk %d does not match its own offsets", s, budget, i)
				}
				if insideWord(text, c.Start) {
					t.Errorf("%s at %d tokens: chunk %d begins inside a word: %q",
						s, budget, i, head(c.Text))
				}
				if insideWord(text, c.End) {
					t.Errorf("%s at %d tokens: chunk %d ends inside a word: %q",
						s, budget, i, tail(c.Text))
				}
			}
		}
	}
}

// The budget is still the budget. A boundary that moved to get out of a word
// must not have made its chunk bigger — the whole reason the move is forward
// and not back.
func TestSnappingOutOfAWordNeverExceedsTheBudget(t *testing.T) {
	text := strings.Repeat("Christoph is responsible for LINSTOR-Gateway and for LINBIT VSAN. ", 12)
	for _, budget := range []int{12, 20, 33, 50} {
		got, err := Split(context.Background(), "r.txt", text, Options{Strategy: Fixed, MaxTokens: budget})
		if err != nil {
			t.Fatalf("at %d tokens: %v", budget, err)
		}
		if len(got) < 2 {
			t.Fatalf("at %d tokens: want several chunks, got %d", budget, len(got))
		}
		for i, c := range got {
			if n := approxTokens(c.Text); n > budget {
				t.Errorf("at %d tokens: chunk %d is %d tokens", budget, i, n)
			}
		}
		covered(t, text, got)
	}
}

// Scripts that put no spaces between words are left alone: every character is
// a boundary, so there is nothing to snap out of, and a snap would walk
// forward through the run looking for a space that is not coming.
func TestAWordSnapDoesNotEatCJK(t *testing.T) {
	text := strings.Repeat("维也纳和波特兰两个办公室，还有捷克、印度和中国的同事。", 10)
	got, err := Split(context.Background(), "cjk.txt", text, Options{Strategy: Fixed, MaxTokens: 24})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(got) < 3 {
		t.Fatalf("want several chunks, got %d", len(got))
	}
	covered(t, text, got)
	for i := 1; i < len(got); i++ {
		if got[i].Start >= got[i-1].End {
			t.Fatalf("chunk %d does not overlap its predecessor: the snap ate the overlap", i)
		}
	}
}

// covered checks the chunks still cover the text end to end, which is the
// property a boundary that moves could quietly break.
func covered(t *testing.T, text string, got []alchemy.Chunk) {
	t.Helper()
	if got[0].Start != 0 {
		t.Errorf("the first chunk starts at %d, so the opening is in no chunk", got[0].Start)
	}
	if last := got[len(got)-1]; last.End != len(text) {
		t.Errorf("the last chunk ends at %d of %d, so the tail is in no chunk", last.End, len(text))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Start > got[i-1].End {
			t.Errorf("nothing covers bytes %d..%d", got[i-1].End, got[i].Start)
		}
	}
}

func head(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

func tail(s string) string {
	if len(s) > 40 {
		return "…" + s[len(s)-40:]
	}
	return s
}
