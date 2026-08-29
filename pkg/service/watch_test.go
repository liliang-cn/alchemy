package service_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// §7.2 and §7.3 are the two requirements on this stream, and both are about
// timing rather than content: the running cost has to arrive while the job can
// still be cancelled, and a conflict has to arrive when it is found. So the
// runner here is held open, and everything is asserted before it is released.
func TestWatchJobStreamsCostAndConflictsWhileTheJobRuns(t *testing.T) {
	hold := newLatch()
	conflict := alchemy.Conflict{
		Kind:    alchemy.ConflictEntityType,
		Subject: "n1",
		Detail:  "the schema says Customer, the contract says Supplier",
		Left:    alchemy.Claim{Statement: "Customer", Provenance: alchemy.Provenance{Source: "schema.sql", Producer: alchemy.ProducerDDL}},
		Right:   alchemy.Claim{Statement: "Supplier", Provenance: alchemy.Provenance{Source: "deal.pdf", Producer: alchemy.ProducerLLMExtract}},
	}
	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, events chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		events <- service.Event{Stage: "chunk", ModelCalls: 3, Counts: alchemy.Counts{Entities: 10}}
		events <- service.Event{Stage: "extract", ModelCalls: 41, Conflict: &conflict,
			ByStage: []alchemy.ModelCall{{Model: "gpt-x", Stage: "extract", Calls: 38, Tokens: 900}}}
		select {
		case <-hold.wait():
		case <-ctx.Done():
			return alchemy.Result{}, ctx.Err()
		}
		return alchemy.Result{}, nil
	}})
	t.Cleanup(hold.release)

	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})

	ctx, cancel := context.WithTimeout(authed(context.Background()), 5*time.Second)
	defer cancel()
	stream, err := cli.WatchJob(ctx, &alchemyv1.WatchJobRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("WatchJob: %v", err)
	}

	var sawConflict, sawCost bool
	var stages []string
	for !(sawConflict && sawCost) {
		e, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv (stages seen: %v): %v", stages, err)
		}
		if e.GetStage() != "" {
			stages = append(stages, e.GetStage())
		}
		if c := e.GetConflict(); c != nil {
			sawConflict = true
			if c.GetSubject() != "n1" || c.GetKind() != alchemyv1.ConflictKind_CONFLICT_KIND_ENTITY_TYPE {
				t.Errorf("conflict = %+v, want the entity_type one about n1", c)
			}
			if c.GetLeft().GetProvenance().GetSource() != "schema.sql" {
				t.Error("the conflict lost its provenance; a reviewer needs the schema on one side and the PDF on the other")
			}
		}
		if e.GetModelCalls() >= 41 {
			sawCost = true
			if len(e.GetModelCallsByStage()) == 0 {
				t.Error("no per-stage breakdown; §7.2 asks which stage is spending it")
			}
		}
	}

	// The job is still running: everything above arrived while there was still
	// something to cancel, which is the entire point of §7.2's running count.
	got, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.GetState() != alchemyv1.JobState_JOB_STATE_RUNNING {
		t.Errorf("state = %v; the events arrived after the job ended, which is too late to act on", got.GetState())
	}
	if got.GetStage() == "" {
		t.Error("the store was never told the stage; a progress display with no stage is a spinner")
	}
}

// The stream ends when the job does, so a client watching to completion learns
// the outcome from the stream rather than from a poll it should not have
// needed.
func TestWatchJobEndsWithTheFinalState(t *testing.T) {
	cli := dial(t, harness{run: staticResult(alchemy.Result{})})
	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})

	ctx, cancel := context.WithTimeout(authed(context.Background()), 5*time.Second)
	defer cancel()
	stream, err := cli.WatchJob(ctx, &alchemyv1.WatchJobRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("WatchJob: %v", err)
	}
	var last alchemyv1.JobState
	for {
		e, err := stream.Recv()
		if err != nil {
			break
		}
		last = e.GetState()
	}
	if last != alchemyv1.JobState_JOB_STATE_SUCCEEDED {
		t.Errorf("last state on the stream = %v, want SUCCEEDED", last)
	}
}

// A client that hangs up must not leave anything behind. The job here stays
// running for the whole test, so anything the watch allocated is still
// allocated when the count is taken — which is what makes the assertion mean
// something.
func TestWatchJobLeaksNothingWhenTheClientDisconnects(t *testing.T) {
	hold := newLatch()
	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, events chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		for {
			select {
			case <-hold.wait():
				return alchemy.Result{}, nil
			case <-ctx.Done():
				return alchemy.Result{}, ctx.Err()
			case events <- service.Event{Stage: "extract"}:
				time.Sleep(time.Millisecond)
			}
		}
	}})
	t.Cleanup(hold.release)

	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_RUNNING)

	settle()
	before := runtime.NumGoroutine()

	for i := 0; i < 25; i++ {
		ctx, cancel := context.WithCancel(authed(context.Background()))
		stream, err := cli.WatchJob(ctx, &alchemyv1.WatchJobRequest{JobId: j.GetId()})
		if err != nil {
			t.Fatalf("WatchJob %d: %v", i, err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("Recv %d: %v", i, err)
		}
		cancel()
	}

	settle()
	if grew := runtime.NumGoroutine() - before; grew > 5 {
		t.Errorf("goroutines grew by %d over 25 abandoned watches; a client that hangs up is leaking one each", grew)
	}
}

func TestWatchJobOnAnUnknownJobIsNotFound(t *testing.T) {
	cli := dial(t, harness{})
	stream, err := cli.WatchJob(authed(context.Background()), &alchemyv1.WatchJobRequest{JobId: "nope"})
	if err == nil {
		_, err = stream.Recv()
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %v, want NotFound (err %v)", got, err)
	}
}

// settle gives goroutines that are on their way out the chance to finish, so
// the count is of what leaked rather than of what had not got round to
// stopping yet.
func settle() {
	for i := 0; i < 20; i++ {
		runtime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
	runtime.GC()
}
