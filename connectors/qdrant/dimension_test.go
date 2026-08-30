package qdrant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The dead end Qdrant has and Postgres does not. A collection created from a
// result with no vectors can never gain one — there is no ALTER — so the
// refusal has to explain the way out rather than only report the mismatch.
func TestAVectoredResultIntoAVectorlessCollectionIsRefusedWithTheWayOut(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	ddl := alchemy.Result{
		Entities: []alchemy.Entity{{ID: "t", Type: "Table", Name: "t", Provenance: ddlProv()}},
	}
	if _, err := l.Load(ctx, ddl, LoadOptions{}); err != nil {
		t.Fatalf("loading the vectorless result: %v", err)
	}

	_, err := l.Load(ctx, smallResult(8), LoadOptions{})
	var de *DimensionError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DimensionError", err)
	}
	if de.Have != 0 || de.Want != 8 {
		t.Errorf("DimensionError = have %d want %d, want have 0 want 8", de.Have, de.Want)
	}
	if !strings.Contains(err.Error(), "collection of its own") {
		t.Errorf("the refusal has to say what to do instead, got: %v", err)
	}
	if n, err := l.Count(ctx, Filter{Kinds: []string{"chunk"}}); err != nil || n != 0 {
		t.Errorf("chunks = %d (err %v), want 0: the refused load must have written nothing", n, err)
	}
}

// A result whose own vectors disagree cannot be stored under either width, and
// the collection is created at one width forever — so this is caught before
// anything exists rather than after half of it does.
func TestAResultWhoseOwnVectorsDisagreeIsRefused(t *testing.T) {
	res := smallResult(8)
	res.Vectors[1].Values = unit(16, 1)
	_, _, err := dimensionOf(res)
	var de *DimensionError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DimensionError", err)
	}
	if !strings.Contains(err.Error(), "chunk 1") {
		t.Errorf("the refusal has to name which part disagreed, got: %v", err)
	}
}

// A vector naming a chunk that is not in the result would leave an embedding
// with no text behind it — searchable, and unreadable when found.
func TestAVectorWithNoChunkIsRefused(t *testing.T) {
	res := smallResult(8)
	res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: 99, Values: unit(8, 2), Model: "embed-4"})
	if _, _, err := dimensionOf(res); err == nil || !strings.Contains(err.Error(), "chunk 99") {
		t.Fatalf("err = %v, want a refusal naming chunk 99", err)
	}
}

// The claim the derived point ID makes is that idempotency needs no locking:
// the same result written by four processes at once is the same point IDs
// carrying the same payloads, so the writes collide harmlessly rather than
// racing. This is that claim against a real server.
func TestFourLoadersWritingTheSameResultAtOnceProduceOneGraph(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res := smallResult(8)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := f.openRaw(t, Config{})
			_, errs[i] = l.Load(ctx, res, LoadOptions{})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("loader %d: %v", i, err)
		}
	}
	l := f.openRaw(t, Config{})
	if n, err := l.Count(ctx, Filter{Kinds: []string{"entity"}}); err != nil || n != 2 {
		t.Errorf("entities = %d (err %v), want 2: four concurrent loads of one result are one graph", n, err)
	}
	loads, err := l.Loads(ctx)
	if err != nil || len(loads) != 1 {
		t.Errorf("Loads = %+v (err %v), want exactly one", loads, err)
	}
}

// §5's findings travel with the graph or the graph is one you merely have.
// Duplicates are the one that has to be joinable to a node — "these two may be
// one thing" is read beside the entity — so they are points, and the rest are
// read whole off the load marker.
func TestTheFindingsTravelWithTheGraph(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	res := smallResult(8)
	res.Duplicates = []alchemy.Duplicate{{
		Signal: alchemy.DuplicateNameAffix, Subject: "CortexDB ~ CortexDB store",
		Detail: "one name contains the other",
		Left:   alchemy.DuplicateSide{ID: "CortexDB", Type: "Store", Name: "CortexDB", Provenance: prov(1)},
		Right:  alchemy.DuplicateSide{ID: "CortexDB2", Type: "Store", Name: "CortexDB store", Provenance: prov(0)},
	}}
	res.Guesses = []alchemy.Guess{{
		Field: "col_3", ChosenAs: "name", Alternatives: []string{"title"}, Reason: "header match", Provenance: prov(0),
	}}
	res.Unread = []alchemy.Unread{{Source: "scan.pdf", Locator: "page 4", Reason: "no text layer and no OCR model"}}
	res.RuleSets = []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{{Name: "authored/type:Service", Told: "Service is a type"}}}}
	res.Counts.Duplicates = 1
	res.Counts.Guesses = 1
	res.Counts.ChunksUnread = 1

	loaded, err := l.Load(ctx, res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	recs, err := l.Records(ctx, Filter{Kinds: []string{"duplicate"}}, 0)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(recs.Duplicates) != 1 {
		t.Fatalf("duplicates = %+v, want 1", recs.Duplicates)
	}
	if got := recs.Duplicates[0].Left.Provenance.Chunk; got != 1 {
		t.Errorf("duplicate left chunk = %d, want 1: a reviewer answering \"are these the same?\" needs which chunk said which", got)
	}
	fnd, err := l.Findings(ctx, loaded.ID)
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(fnd.Guesses) != 1 || fnd.Guesses[0].ChosenAs != "name" {
		t.Errorf("guesses = %+v, want the col_3 → name mapping", fnd.Guesses)
	}
	if len(fnd.Unread) != 1 || fnd.Unread[0].Locator != "page 4" {
		t.Errorf("unread = %+v, want page 4 of scan.pdf", fnd.Unread)
	}
	if len(fnd.RuleSets) != 1 || fnd.RuleSets[0].Rules[0].Told == "" {
		t.Errorf("rule sets = %+v, want the policy the model was told", fnd.RuleSets)
	}
	if fnd.Counts.Duplicates != 1 || fnd.Counts.ChunksUnread != 1 {
		t.Errorf("counts = %+v, want the numbers needed to distrust the graph", fnd.Counts)
	}
	if len(fnd.Lost) == 0 {
		t.Error("the load marker does not say what this store could not keep; a buyer who never saw the return value has no other way to learn it")
	}
}

// Two chunks under one index would derive one point, so the second would
// overwrite the first and the load would report two chunks where the store
// holds one.
//
// It is worth a guard rather than a comment because nothing in pkg/alchemy
// says an index is unique: alchemy.Chunk carries both an Index and a Source,
// which reads as "index within the source", and only pkg/pipeline's adopt()
// renumbers them across sources to make it global. Every connector so far
// silently depends on that invariant. This one checks it.
func TestTwoChunksUnderOneIndexAreRefused(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	res := smallResult(8)
	res.Chunks = append(res.Chunks, alchemy.Chunk{
		Index: 1, Text: "a second file's first chunk", Source: "other.pdf", Strategy: "semantic",
	})
	_, err := l.Load(context.Background(), res, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "chunk 1") {
		t.Fatalf("err = %v, want a refusal naming chunk 1", err)
	}
	if !strings.Contains(err.Error(), "other.pdf") {
		t.Errorf("the refusal has to name both sources so a person can find them, got: %v", err)
	}
}
