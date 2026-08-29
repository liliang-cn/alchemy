package embed

import (
	"context"
	"testing"
)

// §7.2: cost is not optimised for, and it is never hidden. The report is by
// model and stage, because a caller reading one number cannot tell an expensive
// extraction from an expensive embedding pass, and those have different fixes.
func TestModelCallsReportCallsByModelAndStage(t *testing.T) {
	chunks := testChunks(bodies(10)...)
	emb := &fakeEmbedder{name: "text-embedding-fake"}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 3})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got.ModelCalls) != 1 {
		t.Fatalf("got %d cost lines, want one: %+v", len(got.ModelCalls), got.ModelCalls)
	}
	mc := got.ModelCalls[0]
	if mc.Model != emb.Name() {
		t.Errorf("cost is charged to %q, want %q", mc.Model, emb.Name())
	}
	if mc.Stage != "embed" {
		t.Errorf("cost is charged to stage %q, want %q", mc.Stage, "embed")
	}
	if mc.Calls != 4 {
		t.Errorf("reported %d calls, want 4 (10 chunks in batches of 3)", mc.Calls)
	}
	// The port cannot report usage and this provider does not either, so 0 —
	// which is what alchemy.ModelCall documents 0 to mean.
	if mc.Tokens != 0 {
		t.Errorf("reported %d tokens from a provider that reports none", mc.Tokens)
	}
}

// A call that failed was still paid for. A bill that counts only the successes
// understates itself exactly when the caller is most likely to be looking at
// it.
func TestAFailedCallIsStillOnTheBill(t *testing.T) {
	chunks := testChunks(bodies(9)...)
	emb := &fakeEmbedder{errFor: map[string]error{chunks[4].Text: errEndpointDown}}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 3})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got.ModelCalls) != 1 || got.ModelCalls[0].Calls != 3 {
		t.Fatalf("the failed call fell off the bill: %+v", got.ModelCalls)
	}
}

// An embedder that knows what a call cost gets it reported. alchemy.Embedder
// has nowhere to say so, and widening that port would change a contract three
// other stages speak, so the optional interface is offered here instead.
func TestTokensAreReportedWhenTheProviderReportsThem(t *testing.T) {
	chunks := testChunks(bodies(10)...)
	emb := &usageEmbedder{fakeEmbedder: &fakeEmbedder{name: "counts-tokens"}, perText: 11}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got.ModelCalls) != 1 {
		t.Fatalf("got %d cost lines, want one: %+v", len(got.ModelCalls), got.ModelCalls)
	}
	if want := 11 * len(chunks); got.ModelCalls[0].Tokens != want {
		t.Errorf("reported %d tokens, want %d", got.ModelCalls[0].Tokens, want)
	}
	if got.ModelCalls[0].Calls != 3 {
		t.Errorf("reported %d calls, want 3", got.ModelCalls[0].Calls)
	}
	// And the vectors are the ones the same call returned, not a second pass.
	if len(got.Vectors) != len(chunks) {
		t.Fatalf("got %d vectors for %d chunks", len(got.Vectors), len(chunks))
	}
	for i, v := range got.Vectors {
		if !equalVec(v.Values, wantVector(chunks[i].Text)) {
			t.Errorf("vector on chunk %d does not embed that chunk's text", v.Chunk)
		}
	}
}

// Tokens from a call that failed are still tokens.
func TestTokensFromAFailedCallAreStillReported(t *testing.T) {
	chunks := testChunks(bodies(8)...)
	inner := &fakeEmbedder{name: "counts-tokens", errFor: map[string]error{chunks[0].Text: errEndpointDown}}
	emb := &usageEmbedder{fakeEmbedder: inner, perText: 5}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 4})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if want := 5 * len(chunks); got.ModelCalls[0].Tokens != want {
		t.Errorf("reported %d tokens, want %d (the failed call was billed too)", got.ModelCalls[0].Tokens, want)
	}
}
