package embed

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// One batch failing must not lose the job, and must not leave its chunks with
// no vector and no explanation. The chunks in it are named in Unread with the
// endpoint's own words, exactly as §5 requires of source material that could
// not be read: "no vector here" and "an empty page" are different facts and the
// caller has to be able to tell them apart.
func TestAFailedBatchGoesToUnreadAndTheOthersSurvive(t *testing.T) {
	chunks := testChunks(bodies(10)...)
	// Batches of 4 are 0-3, 4-7, 8-9; the middle one fails.
	emb := &fakeEmbedder{errFor: map[string]error{chunks[5].Text: errEndpointDown}}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4})
	if err != nil {
		t.Fatalf("one failed batch lost the whole job: %v", err)
	}
	if len(got.Vectors) != 6 {
		t.Fatalf("got %d vectors, want 6 (the two batches that answered)", len(got.Vectors))
	}
	// The survivors are still on their own chunks: a failed batch in the middle
	// is exactly where a compacting bug would shift everything after it.
	wantChunks := []int{0, 1, 2, 3, 8, 9}
	for i, v := range got.Vectors {
		if v.Chunk != wantChunks[i] {
			t.Errorf("vector %d is on chunk %d, want %d", i, v.Chunk, wantChunks[i])
		}
		if !equalVec(v.Values, wantVector(chunks[v.Chunk].Text)) {
			t.Errorf("vector on chunk %d does not embed chunk %d's text", v.Chunk, v.Chunk)
		}
	}
	if len(got.Unread) != 4 {
		t.Fatalf("got %d unread, want 4 (the failed batch's chunks): %+v", len(got.Unread), got.Unread)
	}
	for i, u := range got.Unread {
		idx := 4 + i
		if !strings.Contains(u.Locator, strconv.Itoa(idx)) {
			t.Errorf("unread %d locates %q, which does not name chunk %d", i, u.Locator, idx)
		}
		if u.Source != chunks[idx].Source {
			t.Errorf("unread %d says source %q, want %q", i, u.Source, chunks[idx].Source)
		}
		// The endpoint's own words, not ours: "embedding failed" costs the
		// reader the one detail that says whether to retry or to fix a key.
		if !strings.Contains(u.Reason, errEndpointDown.Error()) {
			t.Errorf("unread %d reason %q does not carry the endpoint's error", i, u.Reason)
		}
	}
}

// The same rule pkg/extract follows: a result of nothing that took a hundred
// calls is a failure wearing a success's clothes. The Result comes back anyway,
// because §7.2's promise that cost is never hidden is least dispensable on the
// run that spent the most and got the least.
func TestEveryBatchFailingIsAnError(t *testing.T) {
	chunks := testChunks(bodies(9)...)
	errs := map[string]error{}
	for _, c := range chunks {
		errs[c.Text] = errEndpointDown
	}
	emb := &fakeEmbedder{errFor: errs}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4})
	if err == nil {
		t.Fatal("a run that produced no vectors at all reported success")
	}
	if !strings.Contains(err.Error(), errEndpointDown.Error()) {
		t.Errorf("the error does not say why nothing was embedded: %v", err)
	}
	if len(got.Unread) != len(chunks) {
		t.Errorf("got %d unread for %d chunks; every chunk that failed must be named", len(got.Unread), len(chunks))
	}
	if len(got.ModelCalls) != 1 || got.ModelCalls[0].Calls != 3 {
		t.Errorf("the failed run hid what it spent: %+v", got.ModelCalls)
	}
}
