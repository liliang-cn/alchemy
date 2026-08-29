package extract

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// §5 requires these two numbers to stay apart. A chunk the model read and found
// nothing in is a fact about the documents; a chunk whose call failed is a fact
// about the endpoint. Conflating them hides a broken endpoint behind "the
// documents had nothing in them", which is the report that gets believed.
func TestEmptyChunksAndFailedChunksAreDifferentFacts(t *testing.T) {
	llm := &fakeLLM{
		replies: map[int]string{
			0: `{"entities":[{"type":"Node","name":"node-a"}],"relations":[]}`,
			1: `{"entities":[],"relations":[]}`,
			3: `I'm sorry, I can't help with that.`,
		},
		errs: map[int]error{2: errors.New("429 rate limited")},
	}
	got, err := Extract(context.Background(), testChunks("a", "b", "c", "d"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.ChunksEmpty != 1 {
		t.Errorf("ChunksEmpty = %d, want 1 (only chunk 1 was read and found empty)", got.ChunksEmpty)
	}
	if len(got.Unread) != 2 {
		t.Fatalf("Unread = %#v, want the failed call and the unparsable reply", got.Unread)
	}
	// Both unread entries must locate the chunk and say why.
	joined := ""
	for _, u := range got.Unread {
		if u.Source != "architecture.md" {
			t.Errorf("Unread.Source = %q", u.Source)
		}
		if u.Locator == "" || u.Reason == "" {
			t.Errorf("Unread entry names no chunk or no reason: %#v", u)
		}
		joined += u.Locator + " " + u.Reason + "\n"
	}
	for _, want := range []string{"2", "429 rate limited", "3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Unread does not mention %q:\n%s", want, joined)
		}
	}
	// The chunk that worked still worked.
	if len(got.Entities) != 1 || got.Entities[0].Name != "node-a" {
		t.Errorf("one chunk failing lost the others: %#v", got.Entities)
	}
}

// Unread entries are ordered by chunk, whatever order the calls finished in.
func TestUnreadIsOrderedByChunk(t *testing.T) {
	llm := &fakeLLM{
		replies: map[int]string{1: `{"entities":[],"relations":[]}`},
		errs: map[int]error{
			0: errors.New("boom zero"),
			2: errors.New("boom two"),
			3: errors.New("boom three"),
		},
	}
	got, err := Extract(context.Background(), testChunks("a", "b", "c", "d"), Options{
		LLM: llm, Vocabulary: testVocab(), OntologyID: "sds@3", Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := []string{"chunk 0", "chunk 2", "chunk 3"}
	for i, u := range got.Unread {
		if i >= len(want) || !strings.HasPrefix(u.Locator, want[i]) {
			t.Fatalf("Unread[%d].Locator = %q, want %v in order", i, u.Locator, want)
		}
	}
}

// A result of nothing that took a thousand model calls is a failure wearing a
// success's clothes. If every chunk failed, say so.
func TestEveryChunkFailingIsAnError(t *testing.T) {
	llm := &fakeLLM{errs: map[int]error{
		0: errors.New("connection refused"),
		1: errors.New("connection refused"),
	}}
	got, err := Extract(context.Background(), testChunks("a", "b"), testOptions(llm))
	if err == nil {
		t.Fatal("want an error when no chunk could be read, got a clean empty result")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the error does not carry a reason a person could act on: %v", err)
	}
	// The spend and the unread list survive the error: §7.2 says cost is never
	// hidden, and it is least hidden on the run that bought nothing.
	if len(got.Unread) != 2 {
		t.Errorf("Unread = %#v, want both chunks", got.Unread)
	}
	if len(got.ModelCalls) == 0 {
		t.Error("ModelCalls is empty on the run that spent the most and got the least")
	}
}

// No chunks is not the same as every chunk failing. An empty corpus is a fact,
// not a fault, and turning it into an error would make callers guard for it.
func TestNoChunksIsNotAFailure(t *testing.T) {
	llm := &fakeLLM{}
	got, err := Extract(context.Background(), nil, testOptions(llm))
	if err != nil {
		t.Fatalf("Extract on no chunks: %v", err)
	}
	if len(got.Entities) != 0 || len(got.Unread) != 0 || got.ChunksEmpty != 0 {
		t.Errorf("got %#v, want an empty result", got)
	}
}

// §7.2: what the job spent, by model and stage.
func TestModelCallsReportTheSpend(t *testing.T) {
	llm := &fakeLLM{name: "gemini-3.6-flash-high", tokens: 250, replies: map[int]string{
		0: `{"entities":[{"type":"Node","name":"node-a"}],"relations":[]}`,
		1: `{"entities":[],"relations":[]}`,
	}, errs: map[int]error{2: errors.New("500")}}
	got, err := Extract(context.Background(), testChunks("a", "b", "c"), testOptions(llm))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.ModelCalls) != 1 {
		t.Fatalf("ModelCalls = %#v, want one line for one model and one stage", got.ModelCalls)
	}
	mc := got.ModelCalls[0]
	if mc.Model != "gemini-3.6-flash-high" || mc.Stage != "extract" {
		t.Errorf("ModelCall = %q/%q", mc.Model, mc.Stage)
	}
	// The failed call is counted: a call that failed was still paid for, and a
	// cost report that only counts successes understates the bill.
	if mc.Calls != 3 {
		t.Errorf("Calls = %d, want 3 including the one that failed", mc.Calls)
	}
	if mc.Tokens != 500 {
		t.Errorf("Tokens = %d, want 500 (the two calls that reported usage)", mc.Tokens)
	}
}
