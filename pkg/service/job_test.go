package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The happy path, end to end over the wire: a source is uploaded, a job is
// created against it, the runner produces a graph, and GetResult hands it back
// with the numbers §5 says must accompany it.
func TestJobRunsAndItsResultComesBack(t *testing.T) {
	want := alchemy.Result{
		Entities: []alchemy.Entity{{
			ID: "e1", Type: "Customer", Name: "Acme",
			Provenance: alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL},
		}},
		Counts: alchemy.Counts{Entities: 1, Deterministic: 1},
	}
	cli := dial(t, harness{run: staticResult(want)})

	src := upload(t, cli, "schema.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("CREATE TABLE customer (id int);"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})

	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	got, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if n := len(got.GetEntities()); n != 1 {
		t.Fatalf("entities = %d, want 1", n)
	}
	if e := got.GetEntities()[0]; e.GetId() != "e1" || e.GetName() != "Acme" {
		t.Errorf("entity = %+v, want e1/Acme", e)
	}
	if p := got.GetEntities()[0].GetProvenance(); p.GetProducer() != alchemyv1.Producer_PRODUCER_DDL {
		t.Errorf("producer = %v; §5b makes the producer a product guarantee, not a debugging aid", p.GetProducer())
	}
	if got.GetCounts().GetDeterministic() != 1 {
		t.Error("counts did not survive the wire; §5 says the graph is returned with the numbers needed to distrust it")
	}
}

// The runner is handed what the caller supplied and nothing configured
// globally. §6: a buyer's LLM, embedding and OCR endpoints are their business,
// and a service that hardcodes them only works in the environment it was built
// in.
func TestTheRunnerIsGivenTheCallersModelsAndChunking(t *testing.T) {
	seen := make(chan service.JobSpec, 1)
	cli := dial(t, harness{run: func(_ context.Context, _ string, spec service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		seen <- spec
		return alchemy.Result{}, nil
	}})

	src := upload(t, cli, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("# Title\n\ntext"))
	create(t, cli, &alchemyv1.CreateJobRequest{
		SourceIds: []string{src},
		Ontology:  "crm",
		Models: &alchemyv1.Models{
			Llm:      &alchemyv1.ModelEndpoint{Name: "gpt-x", Endpoint: "https://llm.example/v1", ApiKey: "k"},
			Embedder: &alchemyv1.ModelEndpoint{Name: "embed-x", Endpoint: "https://embed.example"},
		},
		Chunking: &alchemyv1.Chunking{Strategy: "heading", Size: 800, Overlap: 80},
		Review:   &alchemyv1.ReviewOptions{Enabled: true, MinConfidence: 0.6},
	})

	var spec service.JobSpec
	select {
	case spec = <-seen:
	case <-time.After(3 * time.Second):
		t.Fatal("the runner was never started")
	}
	if spec.Models.LLM.Name != "gpt-x" || spec.Models.LLM.Endpoint != "https://llm.example/v1" {
		t.Errorf("llm = %+v, want the caller's endpoint", spec.Models.LLM)
	}
	if spec.Models.OCR.Name != "" {
		t.Error("an OCR appeared from nowhere; §5 says an absent one means a scanned page is reported unread")
	}
	if spec.Chunking.Strategy != "heading" || spec.Chunking.Overlap != 80 {
		t.Errorf("chunking = %+v, want the caller's choice (§7.1)", spec.Chunking)
	}
	if !spec.Review.Reviewing || spec.Review.MinConfidence != 0.6 {
		t.Errorf("review options = %+v, want the caller's", spec.Review)
	}
	if len(spec.Sources) != 1 || spec.Sources[0].Path == "" {
		t.Errorf("sources = %+v; §8.4 hands the runner a path, never bytes", spec.Sources)
	}
}

// A result asked for before there is one is a request that will be correct in
// a minute, which is FailedPrecondition and not NotFound: the job exists.
func TestGetResultOnARunningJobIsFailedPrecondition(t *testing.T) {
	hold := newLatch()
	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		select {
		case <-hold.wait():
		case <-ctx.Done():
		}
		return alchemy.Result{}, nil
	}})
	t.Cleanup(hold.release)

	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_RUNNING)

	_, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition (err %v)", got, err)
	}
}
