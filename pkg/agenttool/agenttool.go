// Package agenttool presents a knowledge graph to a language model as a set of
// questions it can call.
//
// It is one layer above pkg/recall and one below any particular agent
// framework: recall says what a store can be asked, and this says how those
// questions are named, described and rendered for a model that has to choose
// between them. Nothing here imports a framework or a store — an MCP server and
// a ReAct loop both build on it, and swapping the store behind it is one line.
//
// THE DESCRIPTIONS ARE THE CODE. Four defects in this repository's read side
// were found by watching an agent answer wrongly through these tools, and every
// one of them was a sentence rather than a function: a tool that named "all" as
// the way to ask for everything while the library took the empty string; one
// that demanded a chunk number its own output does not always carry; a walk
// that returned names and took ids. A wrong description is a bug with the same
// consequences as a wrong query and none of the symptoms, so each one here
// carries the measurement that shaped it.
package agenttool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Prompt is exported as a constant so that a second runtime can reproduce it
// byte for byte. A comparison in which the two sides were prompted differently
// measures the prompts.
const Prompt = `You answer questions about a knowledge graph, using the tools.

Work in steps: find the entity the question is about, walk its neighbours, and
resolve a citation for anything you state.

Rules you must not break:
- Answer ONLY from what the tools return. If the graph does not say, say it does not say.
- Cite every statement as [source#chunk] using what graph_claims gives you.
- Every claim is marked stated or inferred. "stated" came from a schema, a graph
  import, or a named person; "inferred" was proposed by a language model reading
  prose. Say which you relied on.
- Call graph_open_questions before you finish. If an unresolved identity affects
  your answer, say so rather than choosing one side.`

// anchorLimit is how many anchors graph_find returns.
//
// It is small because an anchor search is how a question ENTERS the graph and a
// model reading forty near-misses is a model that has stopped reading. It is
// not how anything is enumerated: that was tried, by an agent with no other
// option, one letter of the alphabet at a time. graph_types and graph_of_type
// are the enumeration path, and graph_find says so when it truncates.
const anchorLimit = 12

// defaultOfTypeLimit is used only when the model gives no limit. It is generous
// because graph_types has just told it the exact count, so a model that omits
// the argument is one that did not read the answer it was given.
const defaultOfTypeLimit = 200

// Tool is one question an agent may ask the graph.
//
// Schema is the JSON Schema object for the arguments, and it is part of the
// tool rather than derived from Do because which arguments are REQUIRED is a
// decision with a measured cost. Twice, a tool here demanded an argument its
// own output does not always contain, and both times the model did the only
// thing left to it and invented one.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Do          func(ctx context.Context, args map[string]any) (any, error)
}

// ReadOnly reports that a tool changes nothing, which every tool here does.
//
// It is a method rather than a field because there is nothing to decide: all
// seven read one finished load, and a load is immutable once complete.
//
// It matters to the agent framework rather than to this package. agent-go
// collapses a duplicate call only for tools DECLARED read-only -- "assuming a
// tool nobody described is safe to skip is the wrong way round" -- and it
// infers the declaration from names containing read, list, get, search, fetch
// or query. Not one of these seven matches, so without saying so out loud they
// were treated as possibly-stateful and every duplicate in a parallel batch was
// executed.
func (t Tool) ReadOnly() bool { return true }

// Graph is the tool set over one load of one store.
//
// The Reader is an interface and nothing here knows which store is behind it,
// which is the point of the example: swapping Neo4j for pgvector or the RDF
// connector is one line in main.
type Graph struct {
	Reader recall.Reader
	Load   string

	mu       sync.Mutex
	calls    []call
	inflight map[string]*flight
	live     int
}

// call is one thing the agent asked, with the two facts that make a trace
// readable.
//
// Both were missing, and their absence cost a wrong conclusion rather than a
// wrong answer. A trace that is a list of names reads as a sequence, so ten
// calls emitted in one message inside ten milliseconds -- four of them the same
// call -- read as an agent re-asking a question it was unhappy with, and were
// written up twice as evidence that a tool had answered the wrong question. The
// timestamps said otherwise: one parallel batch, with duplicates inside it. An
// instrument that cannot tell a retry from a batch will keep producing that
// mistake.
type call struct {
	text string
	// Parallel is true when another call was still in flight when this one
	// started, which is what distinguishes a batch from a sequence.
	Parallel bool
	// Repeat is true when an identical call had already been made in this run.
	Repeat bool
}

// flight is one distinct call, in progress or finished.
//
// Identical calls share it, which is sound rather than an optimisation with a
// caveat: a load is immutable once complete and pkg/recall refuses to serve one
// that is not, so two identical reads in one run cannot disagree. Nothing is
// hidden by it -- every duplicate is still recorded and still rendered as one.
//
// It is single-flight and not a cache, because a cache would not have helped:
// ten identical calls arriving inside ten milliseconds all miss a map that is
// only written when the first one returns.
type flight struct {
	done chan struct{}
	out  any
	err  error
}

// Calls is what was asked of the graph, in order, rendered the way a person
// reading a trace needs it.
//
// It is the output the comparison between two agent runtimes was actually read
// from: the answers were identical on content, and the difference between a
// good run and a bad one was visible only in what was asked. An agent spending
// eight of thirteen calls turning names back into identifiers is not visible in
// its answer, which was correct.
func (g *Graph) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.calls))
	for _, c := range g.calls {
		line := c.text
		switch {
		case c.Repeat && c.Parallel:
			line += "  [repeat, same batch]"
		case c.Repeat:
			line += "  [repeat]"
		case c.Parallel:
			line += "  [parallel]"
		}
		out = append(out, line)
	}
	return out
}

// serve runs one tool call, records it, and lets identical calls share a single
// read of the store.
//
// key is what makes two calls identical and is built from every argument rather
// than from the one the trace displays: graph_of_type("Person", limit 15) and
// the same type with a different limit are different questions, and a key taken
// from the displayed argument alone would answer the second with the first's
// truncated page.
func (g *Graph) serve(name, shown, key string, do func() (any, error)) (any, error) {
	g.mu.Lock()
	if g.inflight == nil {
		g.inflight = map[string]*flight{}
	}
	f, repeat := g.inflight[key]
	g.calls = append(g.calls, call{text: name + "(" + shown + ")", Parallel: g.live > 0, Repeat: repeat})
	g.live++
	if repeat {
		g.mu.Unlock()
		<-f.done
		g.done()
		return f.out, f.err
	}
	f = &flight{done: make(chan struct{})}
	g.inflight[key] = f
	g.mu.Unlock()

	f.out, f.err = do()
	close(f.done)
	g.done()
	return f.out, f.err
}

func (g *Graph) done() {
	g.mu.Lock()
	g.live--
	g.mu.Unlock()
}

// key renders a call for comparison: the tool and every argument, ordered.
func key(name string, a map[string]any) string {
	parts := make([]string, 0, len(a))
	for k := range a {
		parts = append(parts, k)
	}
	sort.Strings(parts)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range parts {
		fmt.Fprintf(&b, "\x00%s=%s", k, str(a[k]))
	}
	return b.String()
}

// schema is the JSON Schema for a tool's arguments: some required, some not.
//
// The two lists are separate because the difference between them has cost this
// example two measured defects, both the same shape -- a tool demanding an
// argument its own output does not always contain, which tells the model to
// invent one. graph_open_questions required a filter and its description named
// "all" as the way to ask for everything, while recall.Unanswered takes the
// EMPTY string for everything and "all" as a literal substring: twenty-nine
// runs in thirty wrote "no unresolved identity questions" over a store holding
// thirteen. graph_cite required a chunk number, while recall.Mark renders a
// chunkless claim as "[team.json]" with no number to give.
func schema(required, optional []string) map[string]any {
	props := map[string]any{}
	for _, f := range append(append([]string{}, required...), optional...) {
		props[f] = map[string]any{"type": "string"}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// str reads an argument a model may not have sent at all.
func str(v any) string { s, _ := v.(string); return s }

// atoi reads a number a model sent as text, falling back to a value the caller
// chooses rather than to zero.
//
// The fallback is a parameter and not a constant because the two callers need
// different ones, and because zero is the answer neither of them wants by
// accident: this is the shape of the defect that made a missing chunk argument
// into "chunk 0". fmt.Sscanf leaves its target untouched when there is nothing
// to read, so a caller that ignored the error got the int's zero value -- which
// is a legal chunk index, and was served as though it had been asked for.
func atoi(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fallback
	}
	return n
}

// Mark is recall.Mark, aliased so the two places this file renders a citation
// marker read the same. A chunkless claim renders as [source] with no #n.
func Mark(source string, chunk int) string { return recall.Mark(source, chunk) }

// sortedKeys keeps two reads of one entity in the same order. An attribute map
// iterated at random would make a context pack that differs between two runs of
// the same question, which is the reproducibility every method in pkg/recall
// orders its results for.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
