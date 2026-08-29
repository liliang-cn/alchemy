package service_test

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The code is the only part of an error most clients read, so the mapping is
// tested as a table rather than asserted where it happens to be convenient.
// The four that matter are four different instructions to a client:
//
//	NotFound           stop asking
//	InvalidArgument    fix the request; retrying is pointless
//	FailedPrecondition the request is fine, the job is not ready
//	ResourceExhausted  §8.4's "try later", and the one to retry on
func TestErrorsMapToCodesDeliberately(t *testing.T) {
	cases := []struct {
		name string
		want codes.Code
		call func(t *testing.T) error
	}{
		{
			name: "a job that never existed",
			want: codes.NotFound,
			call: func(t *testing.T) error {
				cli := dial(t, harness{})
				_, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: "nope"})
				return err
			},
		},
		{
			name: "the result of a job that never existed",
			want: codes.NotFound,
			call: func(t *testing.T) error {
				cli := dial(t, harness{})
				_, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: "nope"})
				return err
			},
		},
		{
			name: "deleting a job that never existed",
			want: codes.NotFound,
			call: func(t *testing.T) error {
				cli := dial(t, harness{})
				_, err := cli.DeleteJob(authed(context.Background()), &alchemyv1.DeleteJobRequest{JobId: "nope"})
				return err
			},
		},
		{
			name: "a job with no sources",
			want: codes.InvalidArgument,
			call: func(t *testing.T) error {
				cli := dial(t, harness{})
				_, err := cli.CreateJob(authed(context.Background()), &alchemyv1.CreateJobRequest{Ontology: "crm"})
				return err
			},
		},
		{
			name: "a job naming a source nobody uploaded",
			want: codes.InvalidArgument,
			call: func(t *testing.T) error {
				cli := dial(t, harness{})
				_, err := cli.CreateJob(authed(context.Background()), &alchemyv1.CreateJobRequest{
					SourceIds: []string{"never-uploaded"}, Ontology: "crm"})
				return err
			},
		},
		{
			// §5: supplying an ontology is required for document sources.
			// There is no unconstrained mode, so this can never be valid.
			name: "a document with no ontology",
			want: codes.InvalidArgument,
			call: func(t *testing.T) error {
				cli := dial(t, harness{})
				src := upload(t, cli, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
				_, err := cli.CreateJob(authed(context.Background()), &alchemyv1.CreateJobRequest{SourceIds: []string{src}})
				return err
			},
		},
		{
			name: "a confidence that is not a confidence",
			want: codes.InvalidArgument,
			call: func(t *testing.T) error {
				cli := dial(t, harness{})
				src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
				_, err := cli.CreateJob(authed(context.Background()), &alchemyv1.CreateJobRequest{
					SourceIds: []string{src}, Ontology: "crm",
					Review: &alchemyv1.ReviewOptions{Enabled: true, MinConfidence: 7}})
				return err
			},
		},
		{
			name: "the result of a job that is held for a person",
			want: codes.FailedPrecondition,
			call: func(t *testing.T) error {
				cli := dial(t, harness{run: staticResult(disputed())})
				src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
				j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
				awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)
				_, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
				return err
			},
		},
		{
			// §8.4: the queue that accepts everything is the queue that OOMs.
			// A rejected job is an operator's problem for a minute; this is
			// the code that tells a client to come back rather than give up.
			name: "a job beyond the declared capacity",
			want: codes.ResourceExhausted,
			call: func(t *testing.T) error {
				hold := newLatch()
				cli := dial(t, harness{
					store: job.New(job.Config{Capacity: 1}),
					run: func(ctx context.Context, _ string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
						select {
						case <-hold.wait():
						case <-ctx.Done():
						}
						return alchemy.Result{}, nil
					},
				})
				t.Cleanup(hold.release)
				src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
				create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
				_, err := cli.CreateJob(authed(context.Background()), &alchemyv1.CreateJobRequest{
					SourceIds: []string{src}, Ontology: "crm"})
				return err
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(t)
			if got := status.Code(err); got != c.want {
				t.Errorf("code = %v, want %v (err %v)", got, c.want, err)
			}
			if status.Convert(err).Message() == "" {
				t.Error("the refusal carries no message; a code with no sentence is a caller guessing")
			}
		})
	}
}

// A retry of a call whose answer was lost must not import the corpus twice.
// §8.3's at-least-once reasoning, one step earlier than the writes it was
// written about.
func TestAnIdempotencyKeyMakesCreateJobARetry(t *testing.T) {
	runs := make(chan string, 4)
	cli := dial(t, harness{run: func(_ context.Context, id string, _ service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		runs <- id
		return alchemy.Result{}, nil
	}})
	src := upload(t, cli, "a.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("x"))
	req := &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm", IdempotencyKey: "nightly-2026-08-30"}

	first := create(t, cli, req)
	second := create(t, cli, req)
	if first.GetId() != second.GetId() {
		t.Errorf("ids %q and %q differ; the retry started a second import", first.GetId(), second.GetId())
	}
	awaitState(t, cli, first.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)
	if n := len(runs); n != 1 {
		t.Errorf("the runner ran %d times, want 1", n)
	}
}
