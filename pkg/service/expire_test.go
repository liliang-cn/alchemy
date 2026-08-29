package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/job"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// §5c: un-reviewed work expires, "otherwise the stateless service quietly
// grows a database of abandoned reviews". pkg/job has the sweeper and the two
// timers; what it does not have is anybody to run it, and a store whose Expire
// is never called is the abandoned database with extra steps.
func TestHeldWorkExpiresAndIsForgotten(t *testing.T) {
	srv, cli := serve(t, harness{
		run:        staticResult(disputed()),
		store:      job.New(job.Config{ReviewTTL: time.Millisecond, ConflictTTL: time.Millisecond}),
		sweepEvery: 2 * time.Millisecond,
	})

	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	waitFor(t, "the held job to expire", func() bool {
		got, err := cli.GetJob(authed(context.Background()), &alchemyv1.GetJobRequest{JobId: j.GetId()})
		return err == nil && got.GetState() == alchemyv1.JobState_JOB_STATE_EXPIRED
	})

	// Expiring the job in the store is only half of it. What §5c refuses to
	// accumulate is the pending result and the corpus behind it.
	waitFor(t, "the expired job's source to be dropped", func() bool {
		_, held := srv.SourceForTest(src)
		return !held
	})
}
