// The eight tools, one per question pkg/recall can answer.
//
// Each carries the measurement that shaped its description, because a
// description is the part of a tool a model actually reads and the part that
// has repeatedly been the defect.
package agenttool

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Tools is the graph as an agent sees it: one tool per question pkg/recall can
// answer, in the order an agent asks them.
func (g *Graph) Tools() []Tool {
	return []Tool{
		g.find(), g.types(), g.ofType(), g.describe(),
		g.claims(), g.contributors(), g.cite(), g.openQuestions(),
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

func (g *Graph) contributors() Tool {
	return Tool{
		Name: "graph_contributors",
		Description: "Which sources had a hand in one entity. Use it before you rely on a person or a " +
			"thing being ONE person or thing: a node built from two sources that both used a bare first " +
			"name may be two different people the graph silently merged. It says which files contributed " +
			"and which of them actually named the node.",
		Schema: schema([]string{"id"}, nil),
		Do: func(ctx context.Context, a map[string]any) (any, error) {
			id := str(a["id"])
			return g.serve("graph_contributors", id, key("graph_contributors", a), func() (any, error) {
				c, err := g.Reader.Contributions(ctx, g.Load, id)
				if err != nil {
					return nil, err
				}
				if len(c.Contributors) == 0 {
					return "nothing in this load contributed to an entity with that id", nil
				}
				var b strings.Builder
				if c.Joined() {
					// Stated as a fact and not as a warning. Two sources agreeing
					// on a full name are corroboration; two agreeing on a first
					// name are a question somebody should look at. A tool that
					// answered "risky" would be making that call for the reader.
					fmt.Fprintf(&b, "%d sources had a hand in this node, so it is a join the graph made:\n", len(c.Contributors))
				} else {
					b.WriteString("one source, so nothing was joined here:\n")
				}
				for _, x := range c.Contributors {
					fmt.Fprintf(&b, "  %s", Mark(x.Source, x.Chunk))
					if x.Stated {
						b.WriteString("  stated")
					} else {
						b.WriteString("  inferred")
					}
					fmt.Fprintf(&b, " (%s)", x.Producer)
					// Only the record that created the node knows what it called
					// it. Reporting the node's name against every contributor
					// would make every join read as unanimous.
					if x.Name != "" {
						fmt.Fprintf(&b, "  named it %q", x.Name)
					} else {
						b.WriteString("  did not name it")
					}
					b.WriteString("\n")
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
