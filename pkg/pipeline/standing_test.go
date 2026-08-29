package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// The two halves of one corpus. Each states something the ontology does not
// declare, and they are separate sources so that the order they are extracted
// in is the pipeline's own — extraction runs per source, one after another —
// rather than a scheduler's.
const (
	firstSection  = "# First\n\nW1 is a Widget.\n"
	secondSection = "# Second\n\nW2 is a Widget.\n"
)

// liveInbox is a review conversation in progress rather than a transcript of
// one that finished: it is asked what is decided *now*, and the answer changes
// while the job runs. §6: "a person working a queue wants their decisions to
// take effect on work still running."
type liveInbox struct {
	mu        sync.Mutex
	decisions []review.Decision
	rules     []review.Rule
	asked     int
}

func (l *liveInbox) Decisions() []review.Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]review.Decision(nil), l.decisions...)
}

func (l *liveInbox) Rules() []review.Rule {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked++
	return append([]review.Rule(nil), l.rules...)
}

func (l *liveInbox) says(d review.Decision, r review.Rule) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.decisions = append(l.decisions, d)
	l.rules = append(l.rules, r)
}

func (l *liveInbox) timesAsked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked
}

// watchfulLLM answers from a script and keeps every system prompt it was sent,
// so a test can ask what the model was actually told. after runs once the
// reply for a chunk matching its key has been produced, which is how a
// reviewer's decision is made to land at a known point in the run.
type watchfulLLM struct {
	replies map[string]string
	after   map[string]func()

	mu      sync.Mutex
	systems []string
}

func (w *watchfulLLM) Name() string { return "fake-llm" }

func (w *watchfulLLM) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return alchemy.LLMResponse{}, err
	}
	w.mu.Lock()
	w.systems = append(w.systems, req.System)
	w.mu.Unlock()
	reply := `{"entities":[],"relations":[]}`
	for _, match := range ordered(w.replies) {
		if strings.Contains(req.Prompt, match) {
			reply = w.replies[match]
			break
		}
	}
	for _, match := range ordered(w.after) {
		if strings.Contains(req.Prompt, match) {
			w.after[match]()
		}
	}
	return alchemy.LLMResponse{Text: reply, Tokens: 3}, nil
}

// toldAbout reports which of the system prompts named the given text.
func (w *watchfulLLM) toldAbout(s string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, sys := range w.systems {
		if strings.Contains(sys, s) {
			n++
		}
	}
	return n
}

// twoSourceRequest is the corpus above, with the LLM the caller supplies.
func twoSourceRequest(t *testing.T, llm alchemy.LLM) Request {
	t.Helper()
	req := regionRequest(t, doc("first.md", firstSection), doc("second.md", secondSection))
	req.Models.LLM = llm
	req.Reviewing = true
	return req
}

// widgetRule is the standing answer a reviewer makes on the Widget question:
// stop asking, and correct ones like it the same way. §5c's `always`, with the
// item it was made from travelling with it.
func widgetRule(t *testing.T, req Request) (review.Rule, review.Decision) {
	t.Helper()
	queue := heldQueue(t, req)
	var item *review.Item
	for i, it := range queue {
		// The first section's question. Subjects are the folded entity ID, so
		// "W1" is written "widget:w1" by the time it reaches a queue.
		if it.Kind == review.KindViolation && strings.Contains(it.Subject, "w1") {
			item = &queue[i]
		}
	}
	if item == nil {
		t.Fatal("the first run asked no question about the Widget, so there is nothing for a reviewer to answer")
	}
	d := review.Decision{
		ItemID: item.ID, Verb: review.VerbAlways, By: "dana",
		Edit: &review.Edit{Type: "Cluster"},
		Note: "these are clusters written up by someone who calls them widgets",
	}
	return review.Rule{Shape: item.Shape, Kind: item.Kind, From: d, Because: item.Summary}, d
}

// THE test §6's first reason for gRPC asks for. A decision arrives while the
// job is running, and the chunk that has not been extracted yet is extracted
// under it while the one that already ran is not.
//
// Both halves matter. Without the first, review is a poll: the decision
// reaches the result and the extractor goes on proposing the thing it was just
// told about. Without the second, a decision silently rewrites history and
// nobody can say which chunk was read under which policy.
func TestARuleMadeMidRunReachesTheChunksThatHaveNotRunYet(t *testing.T) {
	// The reviewer answers a question the corpus already asked once, which is
	// where a real standing answer comes from: a held job hands back a queue.
	rule, decision := widgetRule(t, twoSourceRequest(t, &watchfulLLM{replies: map[string]string{
		"W1": `{"entities":[{"type":"Widget","name":"W1"}],"relations":[]}`,
		"W2": `{"entities":[{"type":"Widget","name":"W2"}],"relations":[]}`,
	}}))

	in := &liveInbox{}
	llm := &watchfulLLM{
		replies: map[string]string{
			"W1": `{"entities":[{"type":"Widget","name":"W1"}],"relations":[]}`,
			"W2": `{"entities":[{"type":"Widget","name":"W2"}],"relations":[]}`,
		},
		// The moment the first source has been read: the reviewer is looking
		// at its queue while the second source is still to come.
		after: map[string]func(){"W1": func() { in.says(decision, rule) }},
	}
	req := twoSourceRequest(t, llm)
	req.Inbox = in

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The model was told, once, and only about the chunk that came after the
	// decision. §6: "an extractor that has already learned this is not an
	// entity should stop proposing it in the next chunk."
	if got := llm.toldAbout("Widget"); got != 1 {
		t.Errorf("the standing answer was in %d system prompts, want exactly the one chunk that ran after it was made", got)
	}

	// And it is a guarantee, not a nudge: this model went on proposing Widget
	// anyway, and the second source's proposal was settled by the rule before
	// it reached the verifier. The first source's was not — it genuinely was
	// extracted before anybody had decided anything.
	var violated []string
	for _, v := range res.Violations {
		violated = append(violated, v.Provenance.Source)
	}
	if len(violated) != 1 || violated[0] != "first.md" {
		t.Errorf("violations came from %v, want only first.md: the rule was made after it and before second.md", violated)
	}

	byName := map[string]alchemy.Entity{}
	for _, e := range res.Entities {
		byName[e.Name] = e
	}
	if got := byName["W1"].Provenance.Rules; got != "" {
		t.Errorf("W1 was extracted before the rule existed but its provenance says %q", got)
	}
	if got := byName["W2"].Provenance.Rules; !strings.Contains(got, rule.Shape) {
		t.Errorf("W2's provenance = %q, want it to name the rule it was extracted under", got)
	}
}

// The other half of "live": the inbox is asked repeatedly, not once. A run
// that read it at the start and cached it would pass the test above only by
// accident of the decision arriving before the first read.
func TestTheInboxIsAskedForEveryChunk(t *testing.T) {
	in := &liveInbox{}
	req := twoSourceRequest(t, &watchfulLLM{replies: map[string]string{
		"W1": `{"entities":[{"type":"Cluster","name":"W1"}],"relations":[]}`,
		"W2": `{"entities":[{"type":"Cluster","name":"W2"}],"relations":[]}`,
	}})
	req.Reviewing = false
	req.Inbox = in

	if _, err := Run(context.Background(), req, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := in.timesAsked(); got < 2 {
		t.Fatalf("the inbox was asked %d times for a two-chunk corpus; a decision made after the first chunk could never reach the second", got)
	}
}

// §5b: a graph explains itself. A run nobody decided anything during is a run
// with nothing to explain, and it must not start claiming otherwise — the
// provenance field is a fact about this run, not a field that is always filled.
func TestARunWithNoStandingAnswersRecordsNone(t *testing.T) {
	req := twoSourceRequest(t, &watchfulLLM{replies: map[string]string{
		"W1": `{"entities":[{"type":"Cluster","name":"W1"}],"relations":[]}`,
		"W2": `{"entities":[{"type":"Cluster","name":"W2"}],"relations":[]}`,
	}})
	req.Reviewing = false
	req.Inbox = &liveInbox{}

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Entities) == 0 {
		t.Fatal("nothing was extracted, so this proves nothing")
	}
	for _, e := range res.Entities {
		if e.Provenance.Rules != "" {
			t.Errorf("entity %q claims it was extracted under %q, and nobody decided anything", e.ID, e.Provenance.Rules)
		}
	}
}
