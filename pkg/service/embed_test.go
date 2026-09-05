package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// read is disputed() with the text the graph was read from, because the bug
// this file is about is invisible without it: a held job's chunks are on the
// pending result and its vectors are not, and until now nothing ever spent
// them. A store loading such a graph reports "Lost: chunks 2" and cannot cite
// the sentence a fact came from, which is the one thing provenance is for.
func read() alchemy.Result {
	res := disputed()
	res.Chunks = []alchemy.Chunk{
		{Index: 0, Source: "deal.pdf", Start: 0, End: 35, Text: "Acme Ltd is the customer of record."},
		{Index: 1, Source: "deal.pdf", Start: 35, End: 71, Text: "Acme Ltd supplies the housings too."},
	}
	// What the extraction already cost. It is here so the assertions below can
	// say the embed was added to the bill rather than replacing it: measured
	// on a real server, a held job answered with Accept came back SUCCEEDED
	// with chunks 2, vectors 0, model calls 1 — the extract call and nothing
	// else, because nothing ever reached the embed stage.
	res.ModelCalls = []alchemy.ModelCall{{Model: "gpt-x", Stage: "extract", Calls: 1}}
	res.Counts.Chunks = 2
	return res
}

// twiceDisputed is two questions on one graph, so that answering one leaves the
// job held on the other. It is what §5c's "embed the text that survived" is
// measured against: a graph that may still change must not be embedded.
func twiceDisputed() alchemy.Result {
	res := read()
	schema := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	prose := alchemy.Provenance{Source: "deal.pdf", Chunk: 1, Producer: alchemy.ProducerLLMExtract, Model: "gpt-x", Confidence: 0.6}
	res.Entities = append(res.Entities,
		alchemy.Entity{ID: "n2", Type: "Product", Name: "Housing", Provenance: schema},
		alchemy.Entity{ID: "n2", Type: "Service", Name: "Housing", Provenance: prose},
	)
	res.Conflicts = append(res.Conflicts, alchemy.Conflict{
		Kind:    alchemy.ConflictEntityType,
		Subject: "n2",
		Detail:  `entity "n2" is typed "Product" by schema.sql and "Service" by deal.pdf`,
		Left:    alchemy.Claim{Statement: "Product", Provenance: schema},
		Right:   alchemy.Claim{Statement: "Service", Provenance: prose},
	})
	res.Counts.Entities = len(res.Entities)
	res.Counts.Conflicts = len(res.Conflicts)
	return res
}

// finisher is a Runner that also implements service.Finisher: it stands in for
// pkg/runner, which is the only thing that holds the caller's models.
//
// It is a separate type from harness on purpose. harness does not implement
// Finisher, so every other test in this package is the fallback case, and the
// one test below that asserts the fallback gets it without arranging anything.
type finisher struct {
	harness

	mu    sync.Mutex
	calls int
	saw   []alchemy.Result
}

func (f *finisher) Embed(_ context.Context, spec service.JobSpec, res alchemy.Result) (alchemy.Result, error) {
	f.mu.Lock()
	f.calls++
	f.saw = append(f.saw, res)
	f.mu.Unlock()

	for _, c := range res.Chunks {
		res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: c.Index, Values: []float32{1, 0}, Model: spec.Models.Embedder.Name})
	}
	res.ModelCalls = append(res.ModelCalls, alchemy.ModelCall{Model: spec.Models.Embedder.Name, Stage: "embed", Calls: 1})
	res.Counts.Vectors = len(res.Vectors)
	return res, nil
}

func (f *finisher) asked() (int, []alchemy.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]alchemy.Result(nil), f.saw...)
}

// withEmbedder creates a job that named an embedder, which is what makes
// missing vectors a defect rather than a configuration (§6: any model may be
// nil, and a job that supplied none wanted a graph and no vectors).
func withEmbedder(t *testing.T, cli alchemyv1.AlchemyClient) *alchemyv1.Job {
	t.Helper()
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	return create(t, cli, &alchemyv1.CreateJobRequest{
		SourceIds: []string{src},
		Ontology:  "crm",
		Models: &alchemyv1.Models{
			Embedder: &alchemyv1.ModelEndpoint{Name: "embed-x", Endpoint: "https://embed.example"},
		},
	})
}

func answer(t *testing.T, cli alchemyv1.AlchemyClient, id, itemID string, verb alchemyv1.ReviewVerb) *alchemyv1.DecideResponse {
	t.Helper()
	res, err := cli.Decide(authed(context.Background()), &alchemyv1.DecideRequest{
		JobId: id,
		Decisions: []*alchemyv1.ReviewDecision{{
			ItemId: itemID, Verb: verb, By: "dana", Note: "the contract is newer than the schema",
		}},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return res
}

func resultOf(t *testing.T, cli alchemyv1.AlchemyClient, id string) *alchemyv1.Result {
	t.Helper()
	got, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: id})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	return got
}

func embedCalls(res *alchemyv1.Result) int {
	n := 0
	for _, c := range res.GetModelCalls() {
		if c.GetStage() == "embed" {
			n += int(c.GetCalls())
		}
	}
	return n
}

// The bug, as a test. §5c says vectors are "recomputed for whatever text
// survives review", and a job that stopped for a person is precisely the job
// whose embedding bill had not been paid yet — so the decision that unblocks it
// is what has to pay it.
func TestAJobHeldByAConflictGetsItsVectorsWhenTheConflictIsAnswered(t *testing.T) {
	fin := &finisher{harness: harness{run: staticResult(read())}}
	cli := dial(t, harness{runner: fin})
	j := withEmbedder(t, cli)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	if got := answer(t, cli, j.GetId(), items[0].GetId(), alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT).GetState(); got != alchemyv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want SUCCEEDED", got)
	}

	res := resultOf(t, cli, j.GetId())
	if len(res.GetVectors()) != 2 {
		t.Fatalf("vectors = %d, want one for each of the 2 surviving chunks; a graph whose facts cannot cite their text is the bug this fixes", len(res.GetVectors()))
	}
	if got := res.GetCounts().GetVectors(); got != 2 {
		t.Errorf("counts.vectors = %d, want 2; §5's numbers are what a reader distrusts the graph with", got)
	}
	if got := res.GetCounts().GetChunks(); got != 2 {
		t.Errorf("counts.chunks = %d, want 2", got)
	}
	for _, v := range res.GetVectors() {
		if v.GetModel() != "embed-x" {
			t.Errorf("vector model = %q, want the embedder the job supplied", v.GetModel())
		}
	}
	// §7.2: the call is real, so the cost report says so. A vector that
	// appeared on the result with nothing on the bill would make the cost
	// report a fiction on exactly the jobs a person worked.
	if got := embedCalls(res); got != 1 {
		t.Errorf("embed model calls = %d, want 1; spend is real and §7.2 does not hide it", got)
	}
	if got := len(res.GetModelCalls()); got != 2 {
		t.Errorf("model call lines = %d, want the extract line the pipeline wrote and the embed line this bought", got)
	}
}

// A rejection removes records, and the vectors are computed after it: §5c's
// "embedding rejected content wastes the call, and embedding before edits means
// the vectors describe text that has since changed". So what reaches the
// embedder is the decided graph, not the one the reviewer was shown.
func TestAJobAnsweredWithRejectEmbedsOnlyWhatSurvived(t *testing.T) {
	fin := &finisher{harness: harness{run: staticResult(read())}}
	cli := dial(t, harness{runner: fin})
	j := withEmbedder(t, cli)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	answer(t, cli, j.GetId(), items[0].GetId(), alchemyv1.ReviewVerb_REVIEW_VERB_REJECT)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	calls, saw := fin.asked()
	if calls != 1 {
		t.Fatalf("the embedder was asked %d times, want once", calls)
	}
	if len(saw[0].Entities) != 1 {
		t.Fatalf("the embedder was handed %d entities, want 1: the rejected record must be gone before anything is bought", len(saw[0].Entities))
	}
	if saw[0].Entities[0].Type != "Customer" {
		t.Errorf("surviving entity is %q, want the Customer the reviewer kept", saw[0].Entities[0].Type)
	}

	res := resultOf(t, cli, j.GetId())
	if len(res.GetVectors()) != 2 {
		t.Fatalf("vectors = %d, want 2: a rejected record does not make its chunk unreadable, and the text that survived is still the text", len(res.GetVectors()))
	}
	if len(res.GetEntities()) != 1 {
		t.Errorf("entities = %d, want the one that survived", len(res.GetEntities()))
	}
}

// §7.3 again, one stage later: a graph that may still change must not be
// embedded. Answering one of two conflicts leaves the job held, and a run that
// embedded there would pay for text a second decision can still edit.
func TestADecisionThatLeavesAnotherConflictOpenDoesNotEmbed(t *testing.T) {
	fin := &finisher{harness: harness{run: staticResult(twiceDisputed())}}
	cli := dial(t, harness{runner: fin})
	j := withEmbedder(t, cli)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	if len(items) != 2 {
		t.Fatalf("items = %d, want the two conflicts", len(items))
	}
	got := answer(t, cli, j.GetId(), items[0].GetId(), alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT)
	if got.GetState() != alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW {
		t.Fatalf("state = %v, want NEEDS_REVIEW; one question is still open", got.GetState())
	}
	if calls, _ := fin.asked(); calls != 0 {
		t.Fatalf("the embedder was asked %d times for a job that is still held", calls)
	}

	// And when the last one is answered, it is spent — once, over the graph as
	// it finally stands.
	answer(t, cli, j.GetId(), items[1].GetId(), alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)
	if calls, _ := fin.asked(); calls != 1 {
		t.Errorf("the embedder was asked %d times, want once: §8.2's bug is paying twice for the identical call", calls)
	}
	if got := len(resultOf(t, cli, j.GetId()).GetVectors()); got != 2 {
		t.Errorf("vectors = %d, want 2", got)
	}
}

// A Runner that cannot embed still finishes the job — but the result says the
// vectors are absent and why. Silence here would be the failure §5 is built
// against: a graph that looks complete, cites nothing, and has no number
// anywhere saying so.
func TestARunnerThatCannotEmbedFinishesAndSaysTheVectorsAreMissing(t *testing.T) {
	cli := dial(t, harness{run: staticResult(read())})
	j := withEmbedder(t, cli)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	answer(t, cli, j.GetId(), items[0].GetId(), alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	res := resultOf(t, cli, j.GetId())
	if len(res.GetVectors()) != 0 {
		t.Fatalf("vectors = %d, want none: this runner has no models to embed with", len(res.GetVectors()))
	}
	if len(res.GetUnread()) != 2 {
		t.Fatalf("unread = %d, want one line per chunk that has no vector", len(res.GetUnread()))
	}
	if got := res.GetCounts().GetChunksUnread(); got != 2 {
		t.Errorf("counts.chunks_unread = %d, want 2; the number and the list a reader checks it against are one fact", got)
	}
	for _, u := range res.GetUnread() {
		if u.GetSource() != "deal.pdf" {
			t.Errorf("unread source = %q, want the file the chunk came from", u.GetSource())
		}
		if !strings.Contains(u.GetLocator(), "chunk") {
			t.Errorf("unread locator = %q, want the chunk it names", u.GetLocator())
		}
		if u.GetReason() == "" {
			t.Error("a chunk with no vector and no reason is exactly what §5 forbids")
		}
	}
}

// A job that supplied no embedder wanted a graph and no vectors, and §6 says
// that is a job rather than a misconfiguration. Nothing is missing, so nothing
// is reported — an Unread line here would teach every caller to ignore them.
func TestAJobThatSuppliedNoEmbedderReportsNothingMissing(t *testing.T) {
	fin := &finisher{harness: harness{run: staticResult(read())}}
	cli := dial(t, harness{runner: fin})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items := findings(t, cli, j.GetId())
	answer(t, cli, j.GetId(), items[0].GetId(), alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT)
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	res := resultOf(t, cli, j.GetId())
	if len(res.GetUnread()) != 0 {
		t.Errorf("unread = %v, want none: nobody asked for vectors", res.GetUnread())
	}
	if calls, _ := fin.asked(); calls != 0 {
		t.Errorf("the embedder was asked %d times for a job that supplied none", calls)
	}
}
