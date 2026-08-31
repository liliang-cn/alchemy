// Package kgagent is a knowledge graph presented to a language model as a
// handful of questions it can ask, and the worked example of pkg/recall.
//
// It is also this repository's measuring instrument, which is the reason it is
// here rather than in somebody's scratch directory. Four defects in the read
// side were found by watching an agent answer wrongly through these tools and
// tracing the wrong answer back to a question the graph could not be asked;
// none of the four was visible from inside the library, because each one was a
// library that answered exactly what it was asked. A harness that keeps finding
// bugs is a thing to keep, and a thing to test.
//
// The tools are separated from the agent that calls them for exactly that
// reason. Every defect listed in tools_test.go was in this file and not in the
// model, and every one of them is reachable with a fake recall.Reader, no
// server and no model endpoint.
//
// It lives in its own module so that agent-go lands neither in the core module,
// whose dependency list DESIGN.md §9 states and internal/claims checks, nor in
// connectors, which a buyer imports in order to load a graph and should not
// have to compile an agent framework for.
package kgagent

import (
	"context"
	"errors"
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

// Tools is the graph as an agent sees it: one tool per question pkg/recall can
// answer, in the order an agent asks them.
func (g *Graph) Tools() []Tool {
	return []Tool{
		g.find(), g.types(), g.ofType(), g.describe(), g.claims(), g.cite(), g.openQuestions(),
	}
}

func (g *Graph) find() Tool {
	return Tool{
		Name: "graph_find",
		Description: "Find entities whose name contains the given text. Returns id, type and name. " +
			"Start here for a question about a named thing.",
		Schema: schema([]string{"name"}, nil),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			name := str(a["name"])
			return g.serve("graph_find", name, key("graph_find", a), func() (any, error) {
				found, err := g.Reader.Find(ctx, g.Load, name, anchorLimit)
				if err != nil {
					return nil, err
				}
				if len(found.Nodes) == 0 {
					return "no entity in the graph has that in its name", nil
				}
				var b strings.Builder
				for _, n := range found.Nodes {
					fmt.Fprintf(&b, "%s  (%s)  id=%s\n", n.Name, n.Type, n.ID)
				}
				if found.Truncated() {
					// Naming what to DO about it. The number alone is what an agent
					// had before: told its list was incomplete, given nothing to
					// complete it with, and left to state it anyway.
					fmt.Fprintf(&b, "(%d more not shown — narrow the text, or use graph_types and "+
						"graph_of_type to read a whole kind)\n", found.Total-len(found.Nodes))
				}
				return b.String(), nil
			})
		},
	}
}

func (g *Graph) types() Tool {
	return Tool{
		Name: "graph_types",
		Description: "The kinds of entity in the graph and how many of each. Call this FIRST for any " +
			"question about what the graph contains, or to list everything of some kind. Takes no arguments.",
		Schema: schema(nil, nil),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			return g.serve("graph_types", "", key("graph_types", a), func() (any, error) {
				types, err := g.Reader.Types(ctx, g.Load)
				if err != nil {
					return nil, err
				}
				if len(types) == 0 {
					return "this load holds no entities", nil
				}
				var b strings.Builder
				for _, t := range types {
					fmt.Fprintf(&b, "%s  %d\n", t.Type, t.Count)
				}
				return b.String(), nil
			})
		},
	}
}

func (g *Graph) ofType() Tool {
	return Tool{
		Name: "graph_of_type",
		Description: "Every entity of one kind, with its id. Use the exact type name from graph_types, " +
			"and pass the count it gave as the limit to get all of them.",
		Schema: schema([]string{"type"}, []string{"limit"}),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			typ := str(a["type"])
			return g.serve("graph_of_type", typ, key("graph_of_type", a), func() (any, error) {
				// The limit is the model's, because graph_types has just told it the
				// number. graph_find's is fixed, and that is how an agent came to
				// answer "list every person" with thirteen of twenty-one.
				limit := atoi(str(a["limit"]), defaultOfTypeLimit)
				if limit <= 0 {
					// Clamped here rather than in atoi, because zero is a legal
					// value for the other caller and the wrong one for this: chunk
					// 0 is a real chunk, and recall refuses a limit of 0 outright.
					limit = defaultOfTypeLimit
				}
				found, err := g.Reader.OfType(ctx, g.Load, typ, limit)
				if err != nil {
					return nil, err
				}
				if len(found.Nodes) == 0 {
					return "no entity in the graph has that type — the type names are what graph_types returns", nil
				}
				var b strings.Builder
				for _, n := range found.Nodes {
					fmt.Fprintf(&b, "%s  id=%s\n", n.Name, n.ID)
				}
				if found.Truncated() {
					fmt.Fprintf(&b, "(%d of %d shown — ask again with limit %d)\n",
						len(found.Nodes), found.Total, found.Total)
				}
				return b.String(), nil
			})
		},
	}
}

func (g *Graph) describe() Tool {
	return Tool{
		Name: "graph_describe",
		Description: "Everything the graph records about one entity: its type, its other names, all the " +
			"fields the source gave it, and who recorded it and when. Call this whenever a node might " +
			"carry detail the one-line claims do not show — dates, numbers, status, anything qualified.",
		Schema: schema([]string{"id"}, nil),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			id := str(a["id"])
			return g.serve("graph_describe", id, key("graph_describe", a), func() (any, error) {
				d, err := g.Reader.Describe(ctx, g.Load, id)
				if err != nil {
					return nil, err
				}
				if d.ID == "" {
					return "this load holds no entity with that id", nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%s (%s) id=%s\n", d.Name, d.Type, d.ID)
				if len(d.Aliases) > 0 {
					fmt.Fprintf(&b, "also called: %s\n", strings.Join(d.Aliases, ", "))
				}
				for _, k := range sortedKeys(d.Attributes) {
					fmt.Fprintf(&b, "  %s: %v\n", k, d.Attributes[k])
				}
				p := d.Provenance
				fmt.Fprintf(&b, "recorded by %s", p.Producer)
				// The asserter and the date, which is the point of returning the
				// whole provenance rather than the four fields a claim carries. A
				// record a named person made seven months ago is a record a reader
				// can weigh and a person can be asked about; without these two it
				// reads as timeless.
				if p.At != "" {
					fmt.Fprintf(&b, " on %s", p.At)
				}
				if p.By != "" {
					fmt.Fprintf(&b, ", asserted by %s", p.By)
				}
				if m := Mark(p.Source, p.Chunk); m != "" {
					fmt.Fprintf(&b, " %s", m)
				}
				b.WriteString("\n")
				return b.String(), nil
			})
		},
	}
}

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

func (g *Graph) claims() Tool {
	return Tool{
		Name: "graph_claims",
		Description: "Every claim one hop from an entity id, with who said it and where. Each line says " +
			"whether it was stated or inferred and ends with [source#chunk], and gives the id of the " +
			"entity at each end — walk another hop by calling this again with one of those ids, rather " +
			"than looking a neighbour up by name.",
		Schema: schema([]string{"id"}, nil),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			id := str(a["id"])
			return g.serve("graph_claims", id, key("graph_claims", a), func() (any, error) {
				claims, err := g.Reader.Claims(ctx, g.Load, id)
				if err != nil {
					return nil, err
				}
				if len(claims) == 0 {
					return "that entity has no neighbours in this graph", nil
				}
				var b strings.Builder
				for _, c := range claims {
					// The rendered claim stays names, because that is what a model
					// weighs the sentence in; the ids follow it as the argument for
					// the next call. Putting ids into the sentence would hand a
					// reader "e17 -[USES]-> e04", which is what recall.Claim keeps
					// names to avoid.
					fmt.Fprintf(&b, "%s   (ids: %s -> %s)\n", c.String(), c.FromID, c.ToID)
				}
				return b.String(), nil
			})
		},
	}
}

func (g *Graph) cite() Tool {
	return Tool{
		Name: "graph_cite",
		Description: "The exact text a claim was extracted from. Give the source file from a " +
			"[source#chunk] marker, and the chunk number if the marker has one. A marker written " +
			"[source] with no number has no chunk to give — omit the argument rather than guess a number.",
		Schema: schema([]string{"source"}, []string{"chunk"}),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			src := str(a["source"])
			// A missing or unreadable chunk is -1, which is what alchemy means
			// by "the producer did not work in chunks". It used to be 0, from a
			// Sscanf whose target keeps its value when there is nothing to read
			// and whose zero is a legal chunk index -- so a model that omitted
			// the number was handed chunk 0 of the file as though it had asked
			// for it, with nothing about the answer looking wrong.
			idx := atoi(str(a["chunk"]), -1)
			return g.serve("graph_cite", Mark(src, idx), key("graph_cite", a), func() (any, error) {
				c, err := g.Reader.Cite(ctx, g.Load, src, idx)
				switch {
				case errors.Is(err, recall.ErrNoChunk):
					// Deliberately not phrased as a refusal. §5b ranks a machine
					// reading something that already asserted a fact ABOVE a model
					// reading prose, so these are the store's strongest records --
					// and while this answer was the ErrNoCitation sentence, they
					// were the ones the model was told to distrust.
					return "that claim was not extracted from a passage of text, so there is nothing to quote. " +
						"It came from a source that already asserted the fact. Cite it as [" + src + "] " +
						"and do not report it as uncited", nil
				case errors.Is(err, recall.ErrNoCitation):
					return "that citation does not resolve in this load, so do not treat it as evidence", nil
				case err != nil:
					return nil, err
				}
				return fmt.Sprintf("%s bytes %d-%d:\n%s", c.Source, c.Start, c.End, c.Text), nil
			})
		},
	}
}

func (g *Graph) openQuestions() Tool {
	return Tool{
		Name: "graph_open_questions",
		Description: "Identity questions nobody has answered: pairs of nodes that may be one node. " +
			"OMIT the argument to see every one of them. Pass a name only to narrow to that name.",
		Schema: schema(nil, []string{"about"}),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			about := str(a["about"])
			return g.serve("graph_open_questions", about, key("graph_open_questions", a), func() (any, error) {
				qs, err := g.Reader.Unanswered(ctx, g.Load, about)
				if err != nil {
					return nil, err
				}
				if len(qs) == 0 {
					return "no unresolved identity questions touch that", nil
				}
				var b strings.Builder
				for _, q := range qs {
					// The subject as well as the detail. recall.Question keeps them
					// apart because the subject is the PAIR -- "left ~ right", as
					// alchemy rendered it -- and the detail is the case against
					// them. Printing only the detail leaves the model reading "one
					// name is the other with a word added" with no way to tell which
					// two nodes, and it read as an answer at all only because
					// alchemy's detail text usually repeats the names.
					fmt.Fprintf(&b, "- %s: %s\n", q.Subject, q.Detail)
				}
				return b.String(), nil
			})
		},
	}
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
