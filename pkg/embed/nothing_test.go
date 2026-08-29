package embed

import (
	"context"
	"strings"
	"testing"
)

// A job may legitimately want a graph and no vectors — §6 says any of the three
// models may be nil — so a nil Embedder is not a stage failing loudly, it is a
// stage that was not asked for. It costs nothing and says nothing.
func TestNilEmbedderAsksForNoVectors(t *testing.T) {
	chunks := testChunks(bodies(5)...)

	got, err := Embed(context.Background(), chunks, Options{})
	if err != nil {
		t.Fatalf("a job that wanted no vectors was failed: %v", err)
	}
	if len(got.Vectors) != 0 || len(got.Unread) != 0 || len(got.ModelCalls) != 0 {
		t.Fatalf("a run with no embedder was not silent: %+v", got)
	}
}

// And "nobody asked for vectors" must not read the same as "the endpoint was
// asked and produced none". The first is a choice, the second is an outage, and
// a caller that cannot tell them apart ships a graph with no vectors believing
// it meant to.
func TestNoEmbedderIsDistinguishableFromAnEmbedderThatProducedNothing(t *testing.T) {
	chunks := testChunks(bodies(5)...)
	errs := map[string]error{}
	for _, c := range chunks {
		errs[c.Text] = errEndpointDown
	}

	quiet, qerr := Embed(context.Background(), chunks, Options{})
	loud, lerr := Embed(context.Background(), chunks, Options{Embedder: &fakeEmbedder{errFor: errs}, BatchSize: 3})

	if qerr != nil {
		t.Fatalf("the nil-embedder run errored: %v", qerr)
	}
	if lerr == nil {
		t.Fatal("the run whose every call failed reported success")
	}
	// Both have no vectors. Everything else about them differs.
	if len(quiet.Unread) != 0 || len(loud.Unread) == 0 {
		t.Errorf("unread does not separate the two: %d vs %d", len(quiet.Unread), len(loud.Unread))
	}
	if len(quiet.ModelCalls) != 0 || len(loud.ModelCalls) == 0 {
		t.Errorf("the cost report does not separate the two: %+v vs %+v", quiet.ModelCalls, loud.ModelCalls)
	}
}

// No chunks is a fact, not a fault — an empty corpus that errored would make
// every caller guard for it.
func TestNoChunksIsNotAFault(t *testing.T) {
	got, err := Embed(context.Background(), nil, Options{Embedder: &fakeEmbedder{}})
	if err != nil {
		t.Fatalf("an empty corpus was failed: %v", err)
	}
	if len(got.Vectors) != 0 || len(got.ModelCalls) != 0 {
		t.Fatalf("an empty corpus bought something: %+v", got)
	}
}

// A vector with no dimensions is not a vector. It is well-formed enough to be
// stored and to be searched against, and it will match everything or nothing
// depending on the index — the kind of wrong that never raises an error. The
// chunk goes to Unread, where a reader can see that it has no embedding and
// why.
func TestAZeroDimensionVectorIsReportedRatherThanStored(t *testing.T) {
	chunks := testChunks(bodies(6)...)
	emb := &fakeEmbedder{emptyFor: map[string]bool{chunks[4].Text: true}}

	got, err := Embed(context.Background(), chunks, Options{Embedder: emb, BatchSize: 3})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for _, v := range got.Vectors {
		if v.Chunk == 4 {
			t.Fatalf("chunk 4 was given an empty vector: %+v", v)
		}
		if len(v.Values) == 0 {
			t.Errorf("chunk %d carries a vector with no dimensions", v.Chunk)
		}
	}
	if len(got.Vectors) != 5 {
		t.Errorf("got %d vectors, want the 5 real ones", len(got.Vectors))
	}
	if len(got.Unread) != 1 || !strings.Contains(got.Unread[0].Locator, "4") {
		t.Fatalf("the chunk with no usable vector was not named: %+v", got.Unread)
	}
	if !strings.Contains(got.Unread[0].Reason, "empty") {
		t.Errorf("the reason does not say what was wrong: %q", got.Unread[0].Reason)
	}
}
