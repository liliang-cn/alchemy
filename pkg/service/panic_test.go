package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// The Runner is somebody else's code, and a pipeline that panics on one badly
// shaped PDF must cost that job and nothing else. A panic crossing this
// boundary would take every other import on the node with it, which is the
// failure §8.4 is arguing against one layer up: an accepted job that dies is
// an operator's problem for an afternoon.
func TestARunnerThatPanicsFailsOnlyItsOwnJob(t *testing.T) {
	cli := dial(t, harness{run: func(_ context.Context, id string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		if strings.HasSuffix(id, "boom") {
			panic("the extractor met a page it did not expect")
		}
		return alchemy.Result{Counts: alchemy.Counts{Entities: 1}}, nil
	}})

	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	bad := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm", IdempotencyKey: "boom"})
	awaitState(t, cli, bad.GetId(), alchemyv1.JobState_JOB_STATE_FAILED)

	got, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: bad.GetId()})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !strings.Contains(got.GetError(), "did not expect") {
		t.Errorf("error = %q; a failed job must say what went wrong or nobody can debug it", got.GetError())
	}

	// The service is still serving, which is the half that matters.
	fine := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, fine.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)
}
