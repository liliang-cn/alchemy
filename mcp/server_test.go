package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/alchemy/mcp"
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// A real client over a real transport against a fake store: the point of these
// is the protocol, not the database.
func connect(t *testing.T, load string) *sdk.ClientSession {
	t.Helper()
	server, _ := mcp.Serve(&store{}, load)
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// Every question pkg/recall answers is offered, under the tool layer's own name
// and description. Writing the descriptions again in this package is how one
// tool comes to say two different things to two clients, and the description is
// the part that has repeatedly been the defect.
func TestTheEightQuestionsAreOffered(t *testing.T) {
	cs := connect(t, "ld-1")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]*sdk.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, want := range []string{
		"graph_find", "graph_types", "graph_of_type", "graph_describe",
		"graph_claims", "graph_contributors", "graph_cite", "graph_open_questions",
	} {
		tool, ok := got[want]
		if !ok {
			t.Errorf("%s is not offered", want)
			continue
		}
		if tool.Description == "" {
			t.Errorf("%s has no description; the description is what the model reads", want)
		}
		// Declared read-only, so a client may collapse a repeated call. A client
		// that has to guess assumes it may not, and the cost of not saying so
		// was measured at twenty-five calls for thirteen distinct questions.
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not declared read-only", want)
		}
	}
	if len(res.Tools) != 8 {
		t.Errorf("%d tools offered, want 8", len(res.Tools))
	}
}

// Two of the eight are reached with no argument at all: graph_open_questions
// asks for everything that way, and graph_cite is handed a marker with no chunk.
func TestAToolWithNoArgumentsIsCallable(t *testing.T) {
	cs := connect(t, "ld-1")
	out, isErr := callTool(t, cs, "graph_open_questions", nil)
	if isErr {
		t.Fatalf("graph_open_questions with no arguments failed: %s", out)
	}
	if !strings.Contains(out, "Nadia") {
		t.Errorf("with no argument the tool answered %q; it must return every open question", out)
	}
}

// What the graph says about its own limits is an ANSWER, not an error.
//
// The tool layer turns a citation that does not resolve, a chunkless claim and
// a name nothing matches into prose written for the model to read — those were
// measured decisions, and marking them isError would put them in the same
// category as a store being down. isError is reserved for what the model cannot
// act on, and the split is already in the tool layer: it renders those three
// and lets a real failure through as an error.
func TestWhatTheGraphCannotAnswerIsStillAnAnswer(t *testing.T) {
	cs := connect(t, "ld-1")

	out, isErr := callTool(t, cs, "graph_cite", map[string]any{"source": "profile.pdf", "chunk": "99"})
	if isErr {
		t.Errorf("an unresolvable citation was reported as a tool failure: %q", out)
	}
	if !strings.Contains(out, "do not treat it as evidence") {
		t.Errorf("the model was not told why: %q", out)
	}

	// The other outcome, which says the opposite about how far to trust the
	// claim and must not read as a refusal.
	out, isErr = callTool(t, cs, "graph_cite", map[string]any{"source": "team.json"})
	if isErr {
		t.Errorf("a chunkless claim was reported as a tool failure: %q", out)
	}
	if !strings.Contains(out, "nothing to quote") || strings.Contains(out, "do not treat it as evidence") {
		t.Errorf("a chunkless claim answered %q", out)
	}
}

// A real failure — the load is not in the store — does come back as an error
// result, with the reason in it. The model is told, rather than handed an empty
// answer it would read as "the graph does not say".
func TestAStoreLevelFailureIsReportedAsOne(t *testing.T) {
	cs := connect(t, "ld-does-not-exist")
	out, isErr := callTool(t, cs, "graph_cite", map[string]any{"source": "profile.pdf", "chunk": "14"})
	if !isErr {
		t.Errorf("citing against an absent load was not reported as a failure: %q", out)
	}
	if !strings.Contains(out, "finished load") {
		t.Errorf("the failure does not say what was wrong: %q", out)
	}
}

// A client that helpfully sends a JSON number must not silently lose it.
//
// Every schema here declares strings, because that is what a model reliably
// produces. A limit arriving as the number 2 would reach the tool layer as a
// float64, be read as the empty string, and fall back to the default — so a
// request for two entities would quietly return two hundred.
func TestANumericArgumentSurvivesAsTheNumberItWas(t *testing.T) {
	cs := connect(t, "ld-1")
	out, isErr := callTool(t, cs, "graph_of_type", map[string]any{"type": "Person", "limit": 2})
	if isErr {
		t.Fatalf("graph_of_type failed: %s", out)
	}
	if n := strings.Count(out, "id=person:"); n != 2 {
		t.Errorf("a limit sent as the JSON number 2 returned %d entities: %q", n, out)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("the truncated page does not say it is one: %q", out)
	}
}

// The load is the operator's choice, fixed when the server starts. A model
// choosing it per call would be choosing which import to answer from, with
// nothing in the question to tell it — which is the bug the load parameter
// exists to prevent, moved one layer out.
func TestTheLoadIsNotSomethingTheModelCanChoose(t *testing.T) {
	cs := connect(t, "ld-1")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("schema of %s: %v", tool.Name, err)
		}
		if strings.Contains(string(b), "load") {
			t.Errorf("%s takes a load argument: %s", tool.Name, b)
		}
	}
	// A server pointed at a load the store does not hold answers nothing,
	// rather than answering from the one it does hold.
	other := connect(t, "ld-does-not-exist")
	if out, _ := callTool(t, other, "graph_find", map[string]any{"name": "Mira"}); strings.Contains(out, "person:mira") {
		t.Errorf("a server scoped to an absent load answered from another one: %q", out)
	}
}

// store is the smallest reader that exercises the protocol.
type store struct{}

func (s *store) people() []recall.Node {
	return []recall.Node{
		{ID: "person:ada", Type: "Person", Name: "Ada"},
		{ID: "person:mira", Type: "Person", Name: "Mira"},
		{ID: "person:nadia", Type: "Person", Name: "Nadia"},
	}
}

func (s *store) Find(_ context.Context, load, name string, limit int) (recall.Found, error) {
	if load != "ld-1" {
		return recall.Found{Nodes: []recall.Node{}}, nil
	}
	var hits []recall.Node
	for _, n := range s.people() {
		if strings.Contains(strings.ToLower(n.Name), strings.ToLower(name)) {
			hits = append(hits, n)
		}
	}
	return pageOf(hits, limit), nil
}

func (s *store) OfType(_ context.Context, load, typ string, limit int) (recall.Found, error) {
	if load != "ld-1" || typ != "Person" {
		return recall.Found{Nodes: []recall.Node{}}, nil
	}
	return pageOf(s.people(), limit), nil
}

func pageOf(hits []recall.Node, limit int) recall.Found {
	found := recall.Found{Total: len(hits), Nodes: hits}
	if found.Nodes == nil {
		found.Nodes = []recall.Node{}
	}
	if len(hits) > limit {
		found.Nodes = hits[:limit]
	}
	return found
}

func (s *store) Types(_ context.Context, load string) ([]recall.TypeCount, error) {
	if load != "ld-1" {
		return nil, nil
	}
	return []recall.TypeCount{{Type: "Person", Count: 3}}, nil
}

func (s *store) Claims(context.Context, string, string) ([]recall.Claim, error) { return nil, nil }

func (s *store) Describe(context.Context, string, string) (recall.Description, error) {
	return recall.Description{}, nil
}

func (s *store) Contributions(context.Context, string, string) (recall.Contributions, error) {
	return recall.Contributions{}, nil
}

func (s *store) Cite(_ context.Context, load, source string, index int) (recall.Citation, error) {
	if load != "ld-1" {
		return recall.Citation{}, fmt.Errorf("%w: %q is not a finished load in this store", recall.ErrNoLoad, load)
	}
	if index < 0 {
		return recall.Citation{}, recall.ErrNoChunk
	}
	if source == "profile.pdf" && index == 14 {
		return recall.Citation{Source: source, Index: 14, Start: 0, End: 5, Text: "Mira."}, nil
	}
	return recall.Citation{}, recall.ErrNoCitation
}

func (s *store) Unanswered(_ context.Context, load, about string) ([]recall.Question, error) {
	if load != "ld-1" {
		return nil, nil
	}
	q := recall.Question{
		Signal: alchemy.DuplicateNameAffix, Subject: "Nadia ~ Nadia Okonkwo",
		Detail: "one name is the other with a word added",
		Left:   "Nadia", Right: "Nadia Okonkwo",
	}
	if about == "" || strings.Contains(strings.ToLower(q.Subject), strings.ToLower(about)) {
		return []recall.Question{q}, nil
	}
	return nil, nil
}
