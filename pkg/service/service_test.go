package service_test

import (
	"context"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// A job is admitted and comes back PENDING with an ID and an expiry. The
// expiry is asserted here rather than left to the store's tests because §5c
// makes it a promise of the interface: a held job that never expires is the
// database this service says it is not.
func TestCreateJobAdmitsAJob(t *testing.T) {
	cli := dial(t, harness{})
	src := upload(t, cli, "schema.sql", alchemyv1.SourceKind_SOURCE_KIND_DDL, []byte("CREATE TABLE t (id int);"))

	got, err := cli.CreateJob(authed(context.Background()), &alchemyv1.CreateJobRequest{
		SourceIds: []string{src},
		Ontology:  "crm",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if got.GetId() == "" {
		t.Error("job has no ID; a caller with no ID cannot ask about its own work")
	}
	if got.GetState() != alchemyv1.JobState_JOB_STATE_PENDING {
		t.Errorf("state = %v, want PENDING", got.GetState())
	}
	if got.GetExpiresAt() == nil {
		t.Error("no expiry: §5c's print queue becomes a filesystem without one")
	}
}
