package runner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// held is the graph a job comes back with when §7.3 stopped it: complete
// except for the vectors, which pkg/pipeline will not spend until the text
// they describe has survived. Everything in this file is about what happens
// to it after a person has answered.
func held() alchemy.Result {
	return alchemy.Result{
		Entities: []alchemy.Entity{{ID: "n1", Type: "Customer", Name: "Acme"}},
		Chunks: []alchemy.Chunk{
			{Index: 0, Source: "deal.pdf", Start: 0, End: 35, Text: "Acme Ltd is the customer of record."},
			{Index: 1, Source: "deal.pdf", Start: 35, End: 71, Text: "Acme Ltd supplies the housings too."},
		},
		ModelCalls: []alchemy.ModelCall{{Model: "gpt-x", Stage: "extract", Calls: 1}},
		Counts:     alchemy.Counts{Entities: 1, Chunks: 2, ChunksEmpty: 1},
	}
}

func embedderSpec() service.JobSpec {
	return service.JobSpec{Models: service.Models{
		Embedder: service.Model{Name: "embed-x", Endpoint: "https://embed.example"},
	}}
}

// The whole of this method: the chunks that survived review get vectors, from
// the embedder the job supplied, and the bill says what it cost.
func TestEmbedSpendsTheVectorsAHeldJobDidNot(t *testing.T) {
	r := newRunner(t)

	out, err := r.Embed(context.Background(), embedderSpec(), held())
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out.Vectors) != 2 {
		t.Fatalf("vectors = %d, want one per surviving chunk", len(out.Vectors))
	}
	for i, v := range out.Vectors {
		if v.Chunk != i {
			t.Errorf("vector %d names chunk %d; a vector belongs to exactly one chunk and says which", i, v.Chunk)
		}
		if v.Model != "embed-x" {
			t.Errorf("vector model = %q, want the embedder the job supplied", v.Model)
		}
	}
	if out.Counts.Vectors != 2 {
		t.Errorf("counts.vectors = %d, want 2; §5's numbers are counted from the slices beside them", out.Counts.Vectors)
	}
	// §7.2: the extract line the pipeline already recorded survives, and the
	// embed line is added to it. A report that lost either would be a total
	// nobody can reconcile with what the endpoint was actually asked.
	if len(out.ModelCalls) != 2 {
		t.Fatalf("model calls = %+v, want the extract line and an embed line", out.ModelCalls)
	}
	if out.ModelCalls[0].Stage != "extract" || out.ModelCalls[1].Stage != "embed" {
		t.Errorf("model calls = %+v, want extract then embed", out.ModelCalls)
	}
	if out.ModelCalls[1].Model != "embed-x" || out.ModelCalls[1].Calls == 0 {
		t.Errorf("embed line = %+v, want the embedder and a real call count", out.ModelCalls[1])
	}
	// ChunksEmpty is the one number that cannot be recomputed from the result,
	// so it accumulates rather than being replaced by this stage's own.
	if out.Counts.ChunksEmpty != 1 {
		t.Errorf("counts.chunks_empty = %d, want the 1 the earlier stages reported", out.Counts.ChunksEmpty)
	}
}

// §6 says any of the three models may be nil and a job with no embedder is a
// job. Asking for vectors nobody ordered would spend the caller's money on a
// graph they asked to have without them.
func TestEmbedBuysNothingWhenTheJobSuppliedNoEmbedder(t *testing.T) {
	r := newRunner(t)

	out, err := r.Embed(context.Background(), service.JobSpec{}, held())
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out.Vectors) != 0 || len(out.Unread) != 0 {
		t.Errorf("vectors = %d, unread = %d; want silence for a job that ordered no vectors", len(out.Vectors), len(out.Unread))
	}
	if len(out.ModelCalls) != 1 {
		t.Errorf("model calls = %+v, want only what the pipeline already spent", out.ModelCalls)
	}
}

// §8.2's retry storm, one stage later than the design describes it: ten nodes
// whose held jobs are all answered at once are ten embed runs against one
// endpoint, and the budget is the only thing between that and the 429s. It
// reaches this stage the way it reaches every other — the model is wrapped
// before the call, so pkg/embed never learns a budget exists.
func TestEmbedLeasesASlotFromTheBudget(t *testing.T) {
	local, err := budget.NewLocal(budget.Config{Limit: 2})
	if err != nil {
		t.Fatalf("budget.NewLocal: %v", err)
	}
	b := &countingBudget{inner: local}
	r, err := New(Config{Factory: &recordingFactory{}, Budget: b})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := r.Embed(context.Background(), embedderSpec(), held()); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := b.acquired.Load(); got == 0 {
		t.Fatal("the embedder was called without leasing a slot; ten nodes answering decisions is the retry storm §8.2 warns about")
	}
	if got := b.model.Load(); got == nil || *got != "embed-x" {
		t.Errorf("budget keyed on %v, want the endpoint name, so the budget and the provenance agree on what an endpoint is", got)
	}
}

// A factory that cannot build the caller's embedder is the caller's error, and
// it is returned rather than swallowed: a graph delivered with no vectors and
// no complaint is the failure this whole change is about.
func TestEmbedReportsAFactoryFailure(t *testing.T) {
	f := &recordingFactory{err: errors.New("no such provider")}
	r, err := New(Config{Factory: f})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, embedErr := r.Embed(context.Background(), embedderSpec(), held())
	if embedErr == nil {
		t.Fatal("Embed hid a factory failure")
	}
	if !errors.Is(embedErr, f.err) {
		t.Errorf("error = %v, want it to wrap the factory's", embedErr)
	}
	// The graph comes back regardless. §7.3's decisions have already been made
	// on it, and throwing away a reviewed graph because an endpoint is
	// unreachable would lose the one part of the job a person did by hand.
	if len(out.Entities) != 1 {
		t.Errorf("entities = %d, want the reviewed graph back even so", len(out.Entities))
	}
}

// countingBudget is budget.Budget with a counter, so a test can say the slot
// was leased rather than only that the vectors arrived.
type countingBudget struct {
	inner    budget.Budget
	acquired atomic.Int64
	model    atomic.Pointer[string]
}

func (b *countingBudget) Acquire(ctx context.Context, model string) (budget.Lease, error) {
	b.acquired.Add(1)
	b.model.Store(&model)
	return b.inner.Acquire(ctx, model)
}
