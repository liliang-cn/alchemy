package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/chunk"
)

// §7.2 gives a caller a running cost so that a job can be "cancelled while it
// runs rather than after it finishes". A cancel that is only noticed at the
// end would make that useless, and a cancel that threw away the bill would
// make the reason for cancelling unanswerable — the caller cancelled because
// of what it was spending, and is owed the number.
func TestCancellationStopsPromptlyAndStillReportsWhatWasSpent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	llm := &scriptLLM{name: "gemini-3.6-flash-high", tokens: 9, replies: map[string]string{
		"eu-west": `{"entities":[{"type":"Cluster","name":"SuperAI"}],"relations":[]}`,
	}}
	// The caller pulls the plug as soon as the first call is made.
	llm.hook = cancel

	req := regionRequest(t, doc("architecture.md",
		"# One\n\nSuperAI is the cluster in eu-west.\n\n# Two\n\nMore prose.\n\n# Three\n\nAnd more.\n"))
	req.Chunking.Overlap = chunk.NoOverlap
	req.Models.LLM = llm
	req.Models.Embedder = &failEmbedder{t: t} // a cancelled job buys no vectors

	res, err := Run(ctx, req, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want it to carry context.Canceled", err)
	}
	if len(res.ModelCalls) == 0 {
		t.Fatalf("a cancelled job reported no cost at all")
	}
	spent := 0
	for _, c := range res.ModelCalls {
		spent += c.Calls
	}
	if spent == 0 {
		t.Errorf("ModelCalls = %+v, want the calls the job made before it was cancelled", res.ModelCalls)
	}
	if len(res.Entities) != 0 {
		t.Errorf("a cancelled job returned a graph: %+v", res.Entities)
	}
}

// Cancelling before the job starts stops it at the first source rather than
// after the corpus has been read.
func TestCancellationBeforeReadingStopsAtTheFirstSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := Request{
		Sources: []Source{{Name: "schema.sql", Kind: alchemy.SourceDDL, Open: openString(twoTables)}},
	}
	res, err := Run(ctx, req, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if len(res.Entities) != 0 {
		t.Errorf("a cancelled job returned a graph: %+v", res.Entities)
	}
}
