package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// specSeenBy runs one CreateJob and hands back the JobSpec the runner was
// given. It goes over the wire rather than calling the conversion directly,
// because the field being tested only earns its keep if it survives the whole
// trip: a proto field nobody reads and a Go field nobody writes both compile.
func specSeenBy(t *testing.T, req *alchemyv1.CreateJobRequest) service.JobSpec {
	t.Helper()
	seen := make(chan service.JobSpec, 1)
	cli := dial(t, harness{run: func(_ context.Context, _ string, spec service.JobSpec, _ chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		seen <- spec
		return alchemy.Result{}, nil
	}})
	req.SourceIds = []string{upload(t, cli, "manual.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("# Title\n\ntext"))}
	create(t, cli, req)
	select {
	case spec := <-seen:
		return spec
	case <-time.After(5 * time.Second):
		t.Fatal("the runner was never given a spec")
		return service.JobSpec{}
	}
}

// TestTheCallerSaysWhichPartTheJobIsExtractedUnder.
//
// §5's ontology is partitioned by provenance and pipeline.Run reads exactly one
// part of it. With no way to say which, a caller whose ontology declares only a
// schema part was refused with "declares no prose part" — a refusal about a
// request they had no way to make.
//
// The unstated case is asserted next to it because it is the compatibility
// promise: a request written before this field existed still means what it
// meant, and the empty string is what the runner turns into prose. This layer
// does not do that turning — it does not know what an ontology is — so what it
// must carry is the caller's silence, unedited.
func TestTheCallerSaysWhichPartTheJobIsExtractedUnder(t *testing.T) {
	const schemaOnly = `{"id":"sds@1","parts":{"schema":{"entities":[{"name":"Table"}],"relations":[]}}}`

	if got := specSeenBy(t, &alchemyv1.CreateJobRequest{Ontology: schemaOnly, Part: "schema"}).Part; got != "schema" {
		t.Errorf("JobSpec.Part = %q, want %q", got, "schema")
	}
	if got := specSeenBy(t, &alchemyv1.CreateJobRequest{Ontology: schemaOnly}).Part; got != "" {
		t.Errorf("JobSpec.Part = %q, want the caller's silence carried unedited", got)
	}
}
