package embed

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// A whitespace-only chunk costs a call and buys a vector that describes
// nothing — and worse than nothing, because every blank chunk embeds to the
// same point and a search for anything vague finds them all. So it is never
// sent.
//
// It is *counted*, not put in Unread: Unread means source material that could
// not be read, and a blank chunk was read perfectly. Conflating them would make
// a healthy job with a leading blank section look like an endpoint failing,
// which is the exact confusion §5 exists to prevent. A count is the right
// weight — a run reporting that a third of its chunks were blank is telling the
// caller their chunker is producing rubbish, and that is worth knowing without
// being an error.
func TestBlankChunksAreCountedAndNeverSent(t *testing.T) {
	chunks := testChunks("first body", "", "second body", "   \n\t ", "third body", "\n\n")
	emb := &fakeEmbedder{}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 2})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for _, sent := range emb.sentTexts() {
		if strings.TrimSpace(sent) == "" {
			t.Errorf("a blank text was sent to the model: %q", sent)
		}
	}
	if got.ChunksEmpty != 3 {
		t.Errorf("ChunksEmpty is %d, want 3", got.ChunksEmpty)
	}
	if len(got.Unread) != 0 {
		t.Errorf("blank chunks were reported as unread, which is a different fact: %+v", got.Unread)
	}
	// The vectors still land on the chunks they came from. Dropping chunks from
	// the middle of the corpus is precisely where an index-vs-position bug
	// hides: chunk 2 is the second vector but the third chunk.
	want := []int{0, 2, 4}
	if len(got.Vectors) != len(want) {
		t.Fatalf("got %d vectors, want %d", len(got.Vectors), len(want))
	}
	for i, v := range got.Vectors {
		if v.Chunk != want[i] {
			t.Fatalf("vector %d is on chunk %d, want %d", i, v.Chunk, want[i])
		}
		if !equalVec(v.Values, wantVector(chunks[want[i]].Text)) {
			t.Errorf("vector on chunk %d does not embed that chunk's text", v.Chunk)
		}
	}
}

// A corpus that is entirely blank bought nothing and failed at nothing. It is
// not the all-batches-failed case: no call was made, so there is no bill and no
// outage to report — only the count, which says plainly what happened.
func TestACorpusOfBlanksIsNotAFailure(t *testing.T) {
	var chunks []alchemy.Chunk
	for _, c := range testChunks("", "  ", "\n") {
		chunks = append(chunks, c)
	}
	emb := &fakeEmbedder{}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb})
	if err != nil {
		t.Fatalf("a blank corpus was reported as a failed run: %v", err)
	}
	if len(emb.sentBatches()) != 0 {
		t.Errorf("a blank corpus still bought %d calls", len(emb.sentBatches()))
	}
	if got.ChunksEmpty != 3 || len(got.Vectors) != 0 || len(got.ModelCalls) != 0 {
		t.Errorf("a blank corpus produced %+v", got)
	}
}
