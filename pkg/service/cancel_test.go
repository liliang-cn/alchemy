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

// §7.2 asks for a job to be cancellable while it runs — an operator watching
// the bill climb needs a way to stop paying — and DeleteJob is that way. The
// runner has to actually stop, not merely be forgotten about.
func TestDeleteJobStopsARunningJob(t *testing.T) {
	stopped := make(chan struct{})
	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		<-ctx.Done()
		close(stopped)
		return alchemy.Result{}, ctx.Err()
	}})

	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_RUNNING)

	if _, err := cli.DeleteJob(authed(context.Background()), &alchemyv1.DeleteJobRequest{JobId: j.GetId()}); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the runner is still going after the job was deleted; a cancelled job that keeps spending is not cancelled")
	}

	_, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: j.GetId()})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("GetJob after delete = %v, want NotFound", got)
	}
}

// §4: the service returns its output and forgets it. A deleted job takes its
// spooled source with it, or the "stateless" service grows a directory instead
// of a database.
func TestDeleteJobForgetsTheSpooledSource(t *testing.T) {
	srv, cli := serve(t, harness{run: staticResult(alchemy.Result{})})
	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	if _, err := cli.DeleteJob(authed(context.Background()), &alchemyv1.DeleteJobRequest{JobId: j.GetId()}); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if _, ok := srv.SourceForTest(src); ok {
		t.Error("the spooled source outlived the job that named it")
	}
}

// Every long-lived RPC has to survive the client walking away. The job is kept
// running throughout so that anything a dropped stream allocated is still
// allocated when the count is taken.
func TestDroppedStreamsLeakNothing(t *testing.T) {
	hold := newLatch()
	cli := dial(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, events chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		for {
			select {
			case <-hold.wait():
				return disputed(), nil
			case <-ctx.Done():
				return alchemy.Result{}, ctx.Err()
			case events <- service.Event{Stage: "extract"}:
				time.Sleep(time.Millisecond)
			}
		}
	}})
	t.Cleanup(hold.release)

	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_RUNNING)

	settle()
	before := runtime.NumGoroutine()

	for i := 0; i < 25; i++ {
		ctx, cancel := context.WithCancel(authed(context.Background()))
		stream, err := cli.Review(ctx)
		if err != nil {
			t.Fatalf("Review %d: %v", i, err)
		}
		if err := stream.Send(&alchemyv1.ReviewDecision{JobId: j.GetId()}); err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		cancel()
	}

	settle()
	if grew := runtime.NumGoroutine() - before; grew > 5 {
		t.Errorf("goroutines grew by %d over 25 abandoned review streams", grew)
	}
}

// Closing the server stops the work rather than orphaning it. Without this,
// a process that shut down cleanly would still be paying a model endpoint.
func TestCloseStopsRunningJobs(t *testing.T) {
	stopped := make(chan struct{})
	srv, cli := serve(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		<-ctx.Done()
		close(stopped)
		return alchemy.Result{}, ctx.Err()
	}})
	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_RUNNING)

	srv.Close()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Close returned with a job still running")
	}
}

// A watcher on a job that is deleted underneath it gets an ending rather than
// a stream that hangs until its deadline.
func TestWatchEndsWhenTheJobIsDeleted(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(authed(context.Background()), 5*time.Second)
	defer cancel()
	stream, err := cli.WatchJob(ctx, &alchemyv1.WatchJobRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("WatchJob: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if _, err := cli.DeleteJob(authed(context.Background()), &alchemyv1.DeleteJobRequest{JobId: j.GetId()}); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the watch is still open on a job that no longer exists")
	}
}
