package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// spool writes a source the way UploadSource would and returns the JobSpec
// source that names it.
func spool(t *testing.T, kind alchemy.SourceKind, name, body string) service.Source {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spooled")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return service.Source{ID: "src-" + name, Name: name, Kind: kind, Path: path}
}

// collect drains the events channel the way pkg/service's pump does, and
// reports what came out. The channel is deliberately not closed by the runner:
// pkg/service owns it and closes it after Run returns.
func collect(t *testing.T) (chan service.Event, func() []service.Event) {
	t.Helper()
	ch := make(chan service.Event, 64)
	var mu sync.Mutex
	var got []service.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range ch {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}
	}()
	return ch, func() []service.Event {
		close(ch)
		<-done
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func newRunner(t *testing.T) *Runner {
	t.Helper()
	r, err := New(Config{Factory: &recordingFactory{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// The end-to-end shape: a spooled DDL file goes in, the graph the deterministic
// reader produced comes out, and no model was ever asked (§2.1's first lesson).
func TestRunReadsASpooledSource(t *testing.T) {
	r := newRunner(t)
	spec := service.JobSpec{Sources: []service.Source{
		spool(t, alchemy.SourceDDL, "schema.sql", "CREATE TABLE users (id INT PRIMARY KEY);"),
	}}

	events, finish := collect(t)
	res, err := r.Run(context.Background(), "job-1", spec, events, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Closing works, so Run did not close it: closing a closed channel panics.
	seen := finish()

	if len(res.Entities) == 0 {
		t.Fatal("no entities; the DDL reader produced nothing")
	}
	if res.Entities[0].Provenance.Source != "schema.sql" {
		t.Fatalf("provenance source = %q, want schema.sql", res.Entities[0].Provenance.Source)
	}
	if len(seen) == 0 {
		t.Fatal("no progress events reached the service")
	}
}

// conflictingDDL declares one table twice with different columns. Nothing in
// the file says which is current, so pkg/source/ddl reports it as a conflict —
// and §7.3 says a conflict holds the job whether or not review mode is on.
const conflictingDDL = `CREATE TABLE users (id INT);
CREATE TABLE users (id INT, email TEXT);`

// THE one thing this package must get right. pipeline.Run answers a held job
// with a *HeldError; §7.3 is computed by pkg/service from the result it is
// handed, so the runner returns the pending graph and a nil error. A runner
// that returned the error would leave the service unable to see the conflicts
// and would turn a held job into a failed one.
func TestRunReturnsAHeldJobAsAPendingResultNotAnError(t *testing.T) {
	r := newRunner(t)
	spec := service.JobSpec{Sources: []service.Source{
		spool(t, alchemy.SourceDDL, "schema.sql", conflictingDDL),
	}}

	events, finish := collect(t)
	res, err := r.Run(context.Background(), "job-held", spec, events, nil)
	seen := finish()

	if err != nil {
		t.Fatalf("Run returned an error for a held job: %v", err)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("the pending result carries no conflicts, so the service cannot see the hold")
	}
	// This is what pkg/service actually tests to decide the hold.
	if len(review.Held(res)) == 0 {
		t.Fatal("review.Held saw nothing to hold the job on")
	}
	if len(res.Entities) == 0 {
		t.Fatal("the pending graph is empty; a reviewer has nothing to decide about")
	}
	// §7.3: an operator watching a long import learns about the conflict when
	// it is found, not at the end.
	found := false
	for _, e := range seen {
		if e.Conflict != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("no conflict was announced on the event stream")
	}
}

// Any other failure is a real failure. §5's rule that a document source needs
// an ontology is the pipeline's to enforce, and the runner must not soften the
// answer into a held job or an empty success.
func TestRunReturnsRealFailures(t *testing.T) {
	r := newRunner(t)
	spec := service.JobSpec{Sources: []service.Source{
		spool(t, alchemy.SourceDocument, "manual.md", "# Cluster\n\nA cluster runs on nodes."),
	}}

	events, finish := collect(t)
	_, err := r.Run(context.Background(), "job-bad", spec, events, nil)
	finish()

	if err == nil {
		t.Fatal("a document job with no ontology was accepted")
	}
	if !strings.Contains(err.Error(), "ontology") {
		t.Fatalf("error = %v, want it to name the missing ontology", err)
	}
}

// A document job is the one that spends money, and §7.2 obliges the service to
// be able to report it as it accrues and to have it in the result at the end.
// This is also the test that the factory is actually consulted: a runner that
// never built the LLM would fail pipeline.validate instead.
func TestRunReportsWhatItSpent(t *testing.T) {
	f := &recordingFactory{}
	r, err := New(Config{Factory: f})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	spec := service.JobSpec{
		Sources: []service.Source{
			spool(t, alchemy.SourceDocument, "manual.md", "# Cluster\n\nA cluster is a set of nodes."),
		},
		Ontology: proseOntology,
		Models:   service.Models{LLM: service.Model{Name: "gpt", Endpoint: "https://llm.example"}},
	}

	events, finish := collect(t)
	res, err := r.Run(context.Background(), "job-doc", spec, events, nil)
	seen := finish()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(res.ModelCalls) == 0 {
		t.Fatal("the result reports no model calls for a job that used a model")
	}
	if res.ModelCalls[0].Model != "gpt" {
		t.Fatalf("model = %q, want the name the factory was asked for", res.ModelCalls[0].Model)
	}
	var running int64
	for _, e := range seen {
		if e.ModelCalls > running {
			running = e.ModelCalls
		}
	}
	if running == 0 {
		t.Fatal("no event carried a running model-call count, so a growing bill is invisible")
	}
}

// pkg/service tells a cancelled job apart from a failed one with
// errors.Is(err, context.Canceled), and reports a shutdown as a defect if that
// test stops working. So the chain has to survive this package's own wrapping.
func TestRunKeepsCancellationRecognisable(t *testing.T) {
	r := newRunner(t)
	spec := service.JobSpec{Sources: []service.Source{
		spool(t, alchemy.SourceDDL, "schema.sql", "CREATE TABLE users (id INT);"),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events, finish := collect(t)
	_, err := r.Run(ctx, "job-cancelled", spec, events, nil)
	finish()

	if err == nil {
		t.Fatal("a cancelled job returned no error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; pkg/service would record this shutdown as a failure", err)
	}
}

// §6's bidirectional stream exists so "a decision reaches an extraction that
// has not run yet", and this pins what that now means: the inbox is asked
// while the job runs, not read once before it starts.
//
// The number asserted is a floor rather than the exact 1 this test used to
// pin, and that change is the point. How many times the inbox is asked is a
// fact about how a corpus happened to be chunked — once per chunk, plus once
// when the review stage assembles the queue — so a test fixing it would fail
// on a chunker default nobody meant to move. What must hold, and what §6
// depends on, is that it is asked *after* work has started: an `always` rule
// recorded while the first chunk is in flight reaches the second. The exact-1
// version of this test was the assertion that it could not, which was true of
// the code and false of the design.
func TestRunAsksTheInboxWhileTheJobIsRunning(t *testing.T) {
	r := newRunner(t)
	in := &countingInbox{}
	spec := service.JobSpec{
		Sources: []service.Source{
			spool(t, alchemy.SourceDocument, "manual.md",
				"# One\n\nA cluster is a set of nodes.\n\n# Two\n\nAnother cluster runs here.\n"),
		},
		Ontology: proseOntology,
		Models:   service.Models{LLM: service.Model{Name: "gpt", Endpoint: "https://llm.example"}},
	}

	events, finish := collect(t)
	if _, err := r.Run(context.Background(), "job-decisions", spec, events, in); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish()

	if got := in.rules.Load(); got < 2 {
		t.Fatalf("Rules() was asked %d times; a rule made after the first chunk could never reach a later one", got)
	}
	// Decisions are still read, for the review stage that carries a
	// reviewer's name onto the graph. A run that stopped asking for them would
	// take §5c's "decisions are part of the result" away.
	if got := in.reads.Load(); got < 1 {
		t.Fatalf("Decisions() was asked %d times; the review stage never read them", got)
	}
}

// A nil inbox is a job nobody is reviewing, and it must not become a job that
// cannot run: the first run of a job that has never been held has nowhere for
// a decision to come from.
func TestRunAcceptsAJobWithNoInbox(t *testing.T) {
	r := newRunner(t)
	spec := service.JobSpec{Sources: []service.Source{
		spool(t, alchemy.SourceDDL, "schema.sql", "CREATE TABLE users (id INT);"),
	}}
	events, finish := collect(t)
	if _, err := r.Run(context.Background(), "job-no-inbox", spec, events, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish()
}

type countingInbox struct{ reads, rules atomic.Int64 }

func (c *countingInbox) Decisions() []review.Decision {
	c.reads.Add(1)
	return nil
}

func (c *countingInbox) Rules() []review.Rule {
	c.rules.Add(1)
	return nil
}
