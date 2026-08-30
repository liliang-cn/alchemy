package sink_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// recorder is a store that stores nothing and remembers everything it was
// asked. It answers the questions this package is responsible for — what the
// envelope does, in what order, and what it hands over — without a database.
type recorder struct {
	ident     sink.Ident
	converged bool
	failOn    string
	calls     []string
	entities  []alchemy.Entity
	relations []alchemy.Relation
	chunks    []sink.Chunk
	findings  sink.Findings
	summary   sink.Summary
	aborted   bool
	lost      []sink.Loss
	commitAs  string
}

func (r *recorder) Begin(_ context.Context, id sink.Ident) (sink.Tx, error) {
	r.ident = id
	r.calls = append(r.calls, "begin")
	if r.failOn == "begin" {
		return nil, errors.New("begin refused")
	}
	return r, nil
}

func (r *recorder) Converged() bool { return r.converged }

func (r *recorder) Entities(_ context.Context, batch []alchemy.Entity) error {
	r.calls = append(r.calls, "entities")
	r.entities = append(r.entities, batch...)
	return r.fail("entities")
}

func (r *recorder) Relations(_ context.Context, batch []alchemy.Relation) error {
	r.calls = append(r.calls, "relations")
	r.relations = append(r.relations, batch...)
	return r.fail("relations")
}

func (r *recorder) Chunks(_ context.Context, batch []sink.Chunk) error {
	r.calls = append(r.calls, "chunks")
	r.chunks = append(r.chunks, batch...)
	return r.fail("chunks")
}

func (r *recorder) Findings(_ context.Context, f sink.Findings) error {
	r.calls = append(r.calls, "findings")
	r.findings = f
	return r.fail("findings")
}

func (r *recorder) Commit(_ context.Context, s sink.Summary) (sink.Report, error) {
	r.calls = append(r.calls, "commit")
	r.summary = s
	if err := r.fail("commit"); err != nil {
		return sink.Report{}, err
	}
	return sink.Report{Load: r.commitAs, Converged: r.commitAs != "", Lost: r.lost}, nil
}

func (r *recorder) Abort(context.Context) error {
	r.calls = append(r.calls, "abort")
	r.aborted = true
	return nil
}

func (r *recorder) fail(stage string) error {
	if r.failOn == stage {
		return errors.New(stage + " refused")
	}
	return nil
}

func graph() alchemy.Result {
	prov := func(c int) alchemy.Provenance {
		return alchemy.Provenance{Source: "a.md", Chunk: c, Producer: alchemy.ProducerLLMExtract, Model: "m"}
	}
	res := alchemy.Result{
		Job: "job-42",
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI", Provenance: prov(0)},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: prov(1)},
		},
		Relations: []alchemy.Relation{{From: "e1", To: "e2", Type: "USES", Provenance: prov(1)}},
		Chunks: []alchemy.Chunk{
			{Index: 0, Source: "a.md", Text: "SuperAI is a system."},
			{Index: 1, Source: "a.md", Text: "SuperAI uses CortexDB."},
		},
		Vectors: []alchemy.Vector{
			{Chunk: 0, Values: []float32{1, 0}, Model: "e5"},
			{Chunk: 1, Values: []float32{0, 1}, Model: "e5"},
		},
		Violations: []alchemy.Violation{{Kind: alchemy.ViolationUnknownEntityType, Subject: "e1", Provenance: prov(0)}},
	}
	res.Counts = res.Derivable()
	return res
}

// §4.1: result identity is above the interface, so "have I loaded this
// already" has one answer instead of four. The name a store files a load under
// is the job that produced it, when the producer named one.
func TestALoadIsNamedAfterTheJobThatProducedTheResult(t *testing.T) {
	r := &recorder{}
	if _, err := sink.Load(context.Background(), r, graph(), sink.Options{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.ident.Load != "job-42" {
		t.Fatalf("Ident.Load = %q, want the job that produced the result", r.ident.Load)
	}
	if r.ident.Digest == "" {
		t.Fatal("Ident.Digest is empty; a store cannot tell a replay from a different graph")
	}
}

// A result nobody named still gets a name, derived from the digest, because a
// load with no name cannot be found again.
func TestAResultWithNoJobIsNamedAfterItsContent(t *testing.T) {
	r := &recorder{}
	res := graph()
	res.Job = ""
	if _, err := sink.Load(context.Background(), r, res, sink.Options{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasPrefix(r.ident.Load, "ld_") || len(r.ident.Load) < 8 {
		t.Fatalf("Ident.Load = %q, want a content-derived name", r.ident.Load)
	}
}

// §8.4 is the whole reason the envelope is Begin → stream → Commit rather than
// Load(Result): a result is paged over gRPC precisely because it does not fit
// in one message, and a sink handed a materialised struct puts the whole graph
// in one process's heap before a byte reaches the store.
func TestRecordsArriveInBatchesRatherThanAllAtOnce(t *testing.T) {
	r := &recorder{}
	if _, err := sink.Load(context.Background(), r, graph(), sink.Options{Batch: 1}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	entityCalls := 0
	for _, c := range r.calls {
		if c == "entities" {
			entityCalls++
		}
	}
	if entityCalls != 2 {
		t.Fatalf("entities arrived in %d call(s), want one per record at Batch 1", entityCalls)
	}
}

// Entities before relations, and it is a contract rather than a convenience:
// every one of the four stores decides what to do with an edge by looking at
// whether both of its ends are there, and a store that met the edge first
// would have to buffer the graph to answer — which is what the envelope exists
// to avoid.
func TestEntitiesArriveBeforeTheRelationsThatNameThem(t *testing.T) {
	r := &recorder{}
	if _, err := sink.Load(context.Background(), r, graph(), sink.Options{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if at(r.calls, "entities") > at(r.calls, "relations") {
		t.Fatalf("calls = %v, want every entity before the first relation", r.calls)
	}
	if at(r.calls, "begin") != 0 || r.calls[len(r.calls)-1] != "commit" {
		t.Fatalf("calls = %v, want begin first and commit last", r.calls)
	}
}

// A chunk and its embedding arrive together, which is what turns "does every
// vector name a chunk that exists" from a whole-result index into something
// the shape makes unaskable. Two of the four stores wrote that check by hand
// and the other two were exposed to it.
func TestAChunkArrivesWithItsOwnVector(t *testing.T) {
	r := &recorder{}
	if _, err := sink.Load(context.Background(), r, graph(), sink.Options{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.chunks) != 2 {
		t.Fatalf("chunks = %d, want both", len(r.chunks))
	}
	for _, c := range r.chunks {
		if len(c.Vector) != 2 || c.Model != "e5" {
			t.Fatalf("chunk %d arrived without its embedding: %+v", c.Index, c)
		}
	}
}

// A chunk nobody embedded keeps its text and arrives with no vector. §5c puts
// the embedding after review, so a chunk that was rejected or produced nothing
// legitimately has none, and a store that refused it would drop the text too.
func TestAChunkWithNoVectorStillArrives(t *testing.T) {
	r := &recorder{}
	res := graph()
	res.Vectors = res.Vectors[:1]
	res.Counts = res.Derivable()
	if _, err := sink.Load(context.Background(), r, res, sink.Options{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.chunks) != 2 || r.chunks[1].Vector != nil {
		t.Fatalf("chunks = %+v, want the unembedded chunk carried with no vector", r.chunks)
	}
}

// The width the store has to bind before it can hold anything is on the
// envelope, because two of the four cannot change it afterwards: Qdrant fixes
// it at collection creation and has no ALTER.
func TestTheVectorWidthIsOnTheEnvelope(t *testing.T) {
	r := &recorder{}
	if _, err := sink.Load(context.Background(), r, graph(), sink.Options{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.ident.Vectors.Dimension != 2 || r.ident.Vectors.Model != "e5" {
		t.Fatalf("Ident.Vectors = %+v, want the width and model this result carries", r.ident.Vectors)
	}
}

// §7.3, asked once for every store. A held graph never reaches Begin, so a
// fifth connector cannot forget.
func TestAHeldResultNeverReachesTheStore(t *testing.T) {
	r := &recorder{}
	res := graph()
	res.Conflicts = []alchemy.Conflict{{Kind: alchemy.ConflictEntityType, Subject: "e1"}}
	res.Counts = res.Derivable()
	if _, err := sink.Load(context.Background(), r, res, sink.Options{}); err == nil {
		t.Fatal("Load accepted a held result")
	}
	if len(r.calls) != 0 {
		t.Fatalf("calls = %v, want the store never opened", r.calls)
	}
}

// Everything else pkg/preflight refuses is refused here too, for the reason
// §4.1 gives: what every sink had to write for itself belongs above the line.
func TestARefusableResultNeverReachesTheStore(t *testing.T) {
	r := &recorder{}
	res := graph()
	res.Chunks = append(res.Chunks, alchemy.Chunk{Index: 1, Source: "b.md"})
	res.Counts = res.Derivable()
	if _, err := sink.Load(context.Background(), r, res, sink.Options{}); err == nil {
		t.Fatal("Load accepted two chunks under one index")
	}
	if len(r.calls) != 0 {
		t.Fatalf("calls = %v, want the store never opened", r.calls)
	}
}

// §4.1: a half-written load has to be observable rather than merely unlikely.
// A failure part-way through aborts, so the store is left saying it is
// incomplete instead of looking finished.
func TestAFailureMidStreamAborts(t *testing.T) {
	for _, stage := range []string{"entities", "relations", "chunks", "findings", "commit"} {
		t.Run(stage, func(t *testing.T) {
			r := &recorder{failOn: stage}
			if _, err := sink.Load(context.Background(), r, graph(), sink.Options{}); err == nil {
				t.Fatalf("Load: err = nil, want the %s failure", stage)
			}
			if !r.aborted {
				t.Fatalf("calls = %v, want an abort so the load says it is unfinished", r.calls)
			}
		})
	}
}

// A store that already holds this exact graph under this name says so, and the
// driver spends nothing writing it again. The mechanics of *how* it knows are
// the store's — MERGE-on-key and ON CONFLICT have nothing in common — and the
// question is the envelope's.
func TestAConvergedLoadWritesNothing(t *testing.T) {
	r := &recorder{converged: true}
	rep, err := sink.Load(context.Background(), r, graph(), sink.Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !rep.Converged {
		t.Error("Report.Converged = false, want the caller told it paid nothing")
	}
	for _, c := range r.calls {
		if c == "entities" || c == "relations" || c == "chunks" {
			t.Fatalf("calls = %v, want no records written for a load already there", r.calls)
		}
	}
}

// §4.1: "a store that cannot represent part of the model must be able to say
// so on the success path". Only one of the four needed it, and it is in for the
// reason that section gives — a guarantee that only holds where it is
// convenient is not a guarantee.
func TestWhatAStoreCouldNotKeepComesBackOnTheSuccessPath(t *testing.T) {
	r := &recorder{lost: []sink.Loss{{What: "relations", Count: 3, Why: "a vector store holds no traversal"}}}
	rep, err := sink.Load(context.Background(), r, graph(), sink.Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rep.Lost) != 1 || rep.Lost[0].Count != 3 {
		t.Fatalf("Lost = %+v, want what the store could not keep", rep.Lost)
	}
}

// The summary is what §5 obliges a graph to carry, handed over once at the end
// rather than repeated per batch — the same argument §8.4 makes for putting it
// on the first page of a stream rather than on every page.
func TestTheSummaryTravelsOnceAtTheEnd(t *testing.T) {
	r := &recorder{}
	res := graph()
	res.RuleSets = []alchemy.RuleSet{{Name: "rs-1"}}
	if _, err := sink.Load(context.Background(), r, res, sink.Options{Batch: 1}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.summary.Counts.Entities != 2 || len(r.summary.RuleSets) != 1 {
		t.Fatalf("summary = %+v, want the counts and the policy", r.summary)
	}
}

// A digest that flipped when a paged result was reassembled in another order
// would refuse every legitimate retry, which is exactly the case §8.4 creates.
func TestTheDigestDoesNotDependOnTheOrderRecordsArrivedIn(t *testing.T) {
	a := graph()
	b := graph()
	b.Entities[0], b.Entities[1] = b.Entities[1], b.Entities[0]
	if sink.Digest(a) != sink.Digest(b) {
		t.Fatal("reassembling a paged result in another order makes it a different load")
	}
}

// And it is not blind to anything a store writes. A result whose only change is
// its policy, or its counts, is a different import: the graph would be the same
// and the store's account of it would not.
func TestTheDigestSeesEverythingAStoreWrites(t *testing.T) {
	base := graph()
	for _, tc := range []struct {
		name string
		edit func(*alchemy.Result)
	}{
		{"chunk text", func(r *alchemy.Result) { r.Chunks[0].Text = "different words" }},
		{"vector", func(r *alchemy.Result) { r.Vectors[0].Values = []float32{9, 9} }},
		{"counts", func(r *alchemy.Result) { r.Counts.ChunksEmpty = 7 }},
		{"rule sets", func(r *alchemy.Result) { r.RuleSets = []alchemy.RuleSet{{Name: "rs-1"}} }},
		{"relation key", func(r *alchemy.Result) { r.Relations[0].Key = "fk_left" }},
		{"provenance", func(r *alchemy.Result) { r.Entities[0].Provenance.Model = "another-model" }},
		{"findings", func(r *alchemy.Result) { r.Violations[0].Detail = "another reason" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := graph()
			tc.edit(&other)
			if sink.Digest(base) == sink.Digest(other) {
				t.Fatalf("the digest is blind to %s, so a changed result replays as unchanged", tc.name)
			}
		})
	}
}

func at(calls []string, want string) int {
	last := -1
	for i, c := range calls {
		if c == want {
			last = i
		}
	}
	return last
}

// A store may finish a load under a different name than the one it was asked
// for, and the driver must not overwrite the answer.
//
// It is not hypothetical: two loaders racing on one graph under two names both
// write, and the one that loses the store's uniqueness check has to resolve to
// the graph that is actually there rather than report a load nobody can find.
// The driver filling in the identity it asked for was a real bug, caught by an
// existing connector test the moment the envelope was extracted.
func TestAStoreMayResolveALoadToAnotherName(t *testing.T) {
	r := &recorder{commitAs: "the-one-that-won"}
	rep, err := sink.Load(context.Background(), r, graph(), sink.Options{Load: "the-one-that-lost"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Load != "the-one-that-won" {
		t.Fatalf("Report.Load = %q, want the name the store resolved it to", rep.Load)
	}
	if !rep.Converged {
		t.Error("Report.Converged = false; the store said this graph was already there")
	}
}

// §7.2's rule one level down: a job reports what it made in model calls even
// when it fails, because "a failed job that reports no calls makes an expensive
// retry look free". A load that died halfway owes an operator the same number —
// how much of it landed — and returning an empty report would make a
// half-written store look like an untouched one.
func TestAFailedLoadStillSaysHowMuchOfItLanded(t *testing.T) {
	r := &recorder{failOn: "chunks"}
	rep, err := sink.Load(context.Background(), r, graph(), sink.Options{})
	if err == nil {
		t.Fatal("Load: err = nil, want the chunk failure")
	}
	if rep.Entities != 2 || rep.Relations != 1 {
		t.Fatalf("report = %+v, want the records that were written before it died", rep)
	}
	if rep.Load != "job-42" {
		t.Errorf("Report.Load = %q, want the load an operator has to go and clean up", rep.Load)
	}
}

// The counts are the driver's, because the driver is what knows how much it
// handed over; a store that recounted them would be answering a question it
// only has a partial view of once a load fails.
func TestTheReportCountsWhatWasHandedOver(t *testing.T) {
	r := &recorder{}
	rep, err := sink.Load(context.Background(), r, graph(), sink.Options{Batch: 1})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Entities != 2 || rep.Relations != 1 || rep.Chunks != 2 || rep.Vectors != 2 || rep.Violations != 1 {
		t.Fatalf("report = %+v, want one count per record handed to the store", rep)
	}
}
