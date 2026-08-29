package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// §7.2 gives WatchJob one job the result cannot do: report the bill while the
// job can still be cancelled. A watcher therefore reads the running total as
// the truth about what has been spent so far — which means the stream must
// never walk it backwards.
//
// A real run against a live gateway is what showed this: the stream climbed to
// five calls, then the terminal event reported zero, and a client that had
// simply kept the last value it was sent would have finished believing the job
// was free. The state event carried the counts and not the spend, so nothing
// was lost in transit and nothing looked wrong — the last message was simply
// answering a different question with a zero.
func TestTheStreamNeverReportsTheJobCostingLessThanItAlreadyHas(t *testing.T) {
	res := alchemy.Result{
		Entities: []alchemy.Entity{{ID: "n1", Type: "Node", Name: "node-a",
			Provenance: alchemy.Provenance{Source: "a.md", Producer: alchemy.ProducerLLMExtract}}},
		Counts:     alchemy.Counts{Entities: 1},
		ModelCalls: []alchemy.ModelCall{{Model: "gpt-x", Stage: "extract", Calls: 4, Tokens: 7331}},
	}
	cli := dial(t, harness{run: func(_ context.Context, _ string, _ service.JobSpec, events chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		events <- service.Event{Stage: "extract", ModelCalls: 4, Counts: alchemy.Counts{Entities: 1},
			ByStage: []alchemy.ModelCall{{Model: "gpt-x", Stage: "extract", Calls: 4, Tokens: 7331}}}
		return res, nil
	}})
	ctx := authed(context.Background())

	src := upload(t, cli, "a.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("# A\n\ntext\n"))
	job, err := cli.CreateJob(ctx, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	watchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := cli.WatchJob(watchCtx, &alchemyv1.WatchJobRequest{JobId: job.GetId()})
	if err != nil {
		t.Fatalf("WatchJob: %v", err)
	}

	var peak, last int64
	var lastByStage []*alchemyv1.ModelCall
	for {
		ev, err := st.Recv()
		if err != nil {
			break
		}
		if ev.GetModelCalls() > peak {
			peak = ev.GetModelCalls()
		}
		last = ev.GetModelCalls()
		if b := ev.GetModelCallsByStage(); len(b) > 0 {
			lastByStage = b
		}
	}

	if peak == 0 {
		t.Fatal("the stream never reported any spend, so the assertion below proves nothing")
	}
	if last < peak {
		t.Errorf("the stream ended reporting %d calls after having reported %d: a running total that walks backwards tells an operator the job got cheaper", last, peak)
	}
	if len(lastByStage) == 0 {
		t.Error("no event carried the per-stage breakdown; §7.2 asks which stage is spending it")
	}
}
