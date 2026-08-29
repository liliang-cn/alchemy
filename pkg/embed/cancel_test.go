package embed

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// §7.2: a job whose bill is growing faster than expected must be cancellable
// while it runs. That promise is worthless twice over if cancelling loses the
// record of what was already spent — the caller cancelled *because* of the
// spend, and the number they wanted is the one the cancel would erase.
func TestCancellationStopsPromptlyAndKeepsTheBill(t *testing.T) {
	chunks := testChunks(bodies(200)...) // 100 batches of 2.
	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	seen := 0
	emb := &fakeEmbedder{before: func([]string) {
		mu.Lock()
		seen++
		enough := seen >= 3
		mu.Unlock()
		if enough {
			cancel()
		}
	}}

	got, err := Embed(ctx, chunks, Options{Embedder: emb, BatchSize: 2, Concurrency: 2})
	if err == nil {
		t.Fatal("a cancelled run reported success; the caller would read the chunks that happened to finish as the corpus")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error does not say the run was cancelled: %v", err)
	}
	// Promptly: the batches that had not started must not be bought. A run that
	// finishes all 100 calls and then reports the cancel has ignored it.
	calls := len(emb.sentBatches())
	if calls > 20 {
		t.Errorf("%d of 100 batches were still sent after the cancel", calls)
	}
	if len(got.ModelCalls) != 1 {
		t.Fatalf("the cancelled run lost the record of what it spent: %+v", got.ModelCalls)
	}
	if got.ModelCalls[0].Calls != calls {
		t.Errorf("the bill says %d calls, the endpoint saw %d", got.ModelCalls[0].Calls, calls)
	}
	// What was paid for is still returned: those vectors are real, and every
	// chunk that has none is named.
	for _, v := range got.Vectors {
		if !equalVec(v.Values, wantVector(chunks[v.Chunk].Text)) {
			t.Errorf("vector on chunk %d does not embed that chunk's text", v.Chunk)
		}
	}
	if len(got.Vectors)+len(got.Unread) != len(chunks) {
		t.Errorf("%d vectors + %d unread does not account for all %d chunks",
			len(got.Vectors), len(got.Unread), len(chunks))
	}
	if len(got.Unread) == 0 || !strings.Contains(got.Unread[len(got.Unread)-1].Reason, context.Canceled.Error()) {
		t.Errorf("the chunks that were dropped do not say why: %+v", got.Unread)
	}
	// The error names the size of what was lost, so a reader knows whether the
	// cancel arrived at the start or the end.
	if !strings.Contains(err.Error(), "200") {
		t.Errorf("the error does not say how much of the corpus was cancelled: %v", err)
	}
}

// A context already cancelled before the call buys nothing at all.
func TestAnAlreadyCancelledContextBuysNothing(t *testing.T) {
	chunks := testChunks(bodies(8)...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	emb := &fakeEmbedder{}

	got, err := Embed(ctx, chunks, Options{Embedder: emb, BatchSize: 2})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want a cancelled error, got %v", err)
	}
	if len(emb.sentBatches()) != 0 {
		t.Errorf("a cancelled run still made %d calls", len(emb.sentBatches()))
	}
	if len(got.Unread) != len(chunks) {
		t.Errorf("got %d unread for %d chunks; a chunk with no vector is never silent", len(got.Unread), len(chunks))
	}
}
