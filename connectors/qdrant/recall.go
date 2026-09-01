package qdrant

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"

	"github.com/liliang-cn/alchemy/connectors/internal/contributions"
)

// Loader is a recall.Reader.
//
// A vector store answering graph questions needs an argument, and this is it:
// alchemy loads a graph into the store a buyer already runs, and until now two
// of the five could be written to and not read back. A buyer who chose Qdrant
// got the write side of the product and none of the read side, which is where
// the context pack — the reason any of it is worth buying — is built.
//
// Search and Around stay beside these eight and are not folded in, for the
// reason pkg/recall gives: they answer "which text is about this" from an
// embedding, which is a different question with a different input. Both
// surfaces exist and this store is the one that has both.
//
// SIX OF THE EIGHT ARE INDEX LOOKUPS and two are scans, and the split is worth
// stating because payloadIndexes already states the rule: entity_id, type,
// rel_from, rel_to, chunk_index and source are indexed, so Describe, OfType,
// Claims, Cite, Contributions and Unanswered are filters the server resolves.
// Find and Types are not, and cannot be. Find is a case-insensitive SUBSTRING
// match — Qdrant matches whole values or full-text tokens, neither of which is
// that — and Types is an aggregation this store has no operator for. Both walk
// the load's entities. On the corpus sizes §8 is about that is a real cost, and
// naming it here is the alternative to a buyer discovering it.
var _ recall.Reader = (*Loader)(nil)

// finished reports whether one load is present and complete.
//
// Every read below asks. Filter.Loads is taken at its word by resolve — a
// caller naming a load gets that load — which is right for the query surface
// and wrong for this one: recall.Reader must never serve a load that is still
// arriving, because a partial graph reported as a whole one is the confident
// wrong answer the whole design is arranged against.
func (l *Loader) finished(ctx context.Context, load string) (bool, error) {
	ids, err := l.completeIDs(ctx)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == load {
			return true, nil
		}
	}
	return false, nil
}

// noLoad is the error the three methods that distinguish an absent load return.
func noLoad(load string) error {
	return fmt.Errorf("%w: %q is not a finished load in this collection; "+
		"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
		recall.ErrNoLoad, load)
}

// within is the filter every read starts from: one finished load, one kind of
// point, plus whatever the question adds.
func within(load string, k kind, extra ...map[string]any) map[string]any {
	must := append([]map[string]any{match(keyKind, string(k)), match(keyLoad, load)}, extra...)
	return map[string]any{"must": must}
}

// Find returns the entities of one load whose name contains name.
//
// A scan of the load's entities with the match done here, because the match is
// a case-insensitive substring and Qdrant has no such condition: `match` is
// whole-value and full-text `match` is tokens. Either would be a different
// question answered under this one's name — a search for "node_connections"
// that missed, or one for "ada" that matched "Ada Lovelace" and not "Adam".
func (l *Loader) Find(ctx context.Context, load, name string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("qdrant: limit = %d is not a number of anchors", limit)
	}
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return recall.Found{Nodes: []recall.Node{}}, err
	}
	pts, err := l.scroll(ctx, within(load, kindEntity), 0)
	if err != nil {
		return recall.Found{}, fmt.Errorf("qdrant: find %q in load %q: %w", name, load, err)
	}
	needle := strings.ToLower(name)
	var hits []recall.Node
	for _, p := range pts {
		n := node(p.Payload)
		if strings.Contains(strings.ToLower(n.Name), needle) {
			hits = append(hits, n)
		}
	}
	return page(hits, limit), nil
}

// Types is the vocabulary of one load.
//
// Counted here rather than asked of the server, which has no aggregation this
// package can reach. Count(Filter{Type: x}) is an indexed question and would be
// cheap, but it needs the list of types to ask it about, and getting the list
// is the scan.
func (l *Loader) Types(ctx context.Context, load string) ([]recall.TypeCount, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return nil, err
	}
	pts, err := l.scroll(ctx, within(load, kindEntity), 0)
	if err != nil {
		return nil, fmt.Errorf("qdrant: types in load %q: %w", load, err)
	}
	counts := map[string]int{}
	for _, p := range pts {
		counts[str(p.Payload[keyType])]++
	}
	out := make([]recall.TypeCount, 0, len(counts))
	for t, n := range counts {
		out = append(out, recall.TypeCount{Type: t, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// OfType reads out one class, and unlike Find it is an indexed lookup: `type`
// is on payloadIndexes because Filter has always exposed it.
//
// Compared exactly rather than folded, for the reason every store gives: a type
// is declared by an ontology, and Find's case-insensitivity is for text
// somebody typed.
func (l *Loader) OfType(ctx context.Context, load, typ string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("qdrant: limit = %d is not a number of entities", limit)
	}
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return recall.Found{Nodes: []recall.Node{}}, err
	}
	pts, err := l.scroll(ctx, within(load, kindEntity, match(keyType, typ)), 0)
	if err != nil {
		return recall.Found{}, fmt.Errorf("qdrant: entities of type %q in load %q: %w", typ, load, err)
	}
	hits := make([]recall.Node, 0, len(pts))
	for _, p := range pts {
		hits = append(hits, node(p.Payload))
	}
	return page(hits, limit), nil
}

// Describe returns one entity whole.
func (l *Loader) Describe(ctx context.Context, load, id string) (recall.Description, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Description{}, err
	}
	if !ok {
		return recall.Description{}, noLoad(load)
	}
	pts, err := l.scroll(ctx, within(load, kindEntity, match(keyEntityID, id)), 1)
	if err != nil {
		return recall.Description{}, fmt.Errorf("qdrant: describe %q in load %q: %w", id, load, err)
	}
	if len(pts) == 0 {
		return recall.Description{}, nil
	}
	p := pts[0].Payload
	return recall.Description{
		ID:         id,
		Type:       str(p[keyType]),
		Name:       str(p[keyName]),
		Aliases:    aliasesOf(p[keyAliases]),
		Attributes: attrs(p[keyAttributes]),
		Provenance: readProvenance(p),
	}, nil
}

// Claims returns every extracted edge touching one entity, in either direction,
// each carrying the provenance of the EDGE.
//
// Two indexed conditions joined by `should`, which is Qdrant's OR: rel_from and
// rel_to are both on payloadIndexes because Around already walks them, so a
// one-hop walk here is the same two lookups a graph database would do.
//
// The endpoint names come off the edge's own payload — rel_from_name and
// rel_to_name, written beside the ids — so a claim reads as a sentence without
// a second round trip per endpoint, and the ids travel with them so a walk can
// continue without one either.
func (l *Loader) Claims(ctx context.Context, load, id string) ([]recall.Claim, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return []recall.Claim{}, err
	}
	flt := within(load, kindRelation)
	// `should` beside `must` is Qdrant's OR-within-an-AND: the kind and the
	// load must hold, and at least one end must be this entity.
	flt["should"] = []map[string]any{match(keyRelFrom, id), match(keyRelTo, id)}
	pts, err := l.scroll(ctx, flt, 0)
	if err != nil {
		return nil, fmt.Errorf("qdrant: claims about %q in load %q: %w", id, load, err)
	}
	out := make([]recall.Claim, 0, len(pts))
	for _, p := range pts {
		pay := p.Payload
		from := recall.Endpoint{ID: str(pay[keyRelFrom]), Name: str(pay[keyRelFromName])}
		to := recall.Endpoint{ID: str(pay[keyRelTo]), Name: str(pay[keyRelToName])}
		// A dangling endpoint has no name because the load does not describe
		// it. Rendering the id is what pgvector's coalesce does, and it keeps
		// "a claim about something this load does not describe" readable
		// instead of blank.
		if from.Name == "" {
			from.Name = from.ID
		}
		if to.Name == "" {
			to.Name = to.ID
		}
		out = append(out, recall.NewClaim(from, to, str(pay[keyType]), readProvenance(pay)))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.To != b.To {
			return a.To < b.To
		}
		if a.From != b.From {
			return a.From < b.From
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Chunk < b.Chunk
	})
	return out, nil
}

// Cite resolves one [source#index] marker against one load.
//
// Three outcomes. ErrNoChunk when the marker carries no number, which is an
// ordinary answer and the common one; ErrNoCitation when this load holds no
// such chunk; ErrNoLoad when there is no finished load of that name.
func (l *Loader) Cite(ctx context.Context, load, source string, index int) (recall.Citation, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Citation{}, err
	}
	if !ok {
		return recall.Citation{}, noLoad(load)
	}
	if index < 0 {
		return recall.Citation{}, fmt.Errorf("%w: the claim citing %q carries no chunk number, so load %q "+
			"holds no text to quote for it — the claim is not weakened by that, and must not be reported as uncited",
			recall.ErrNoChunk, source, load)
	}
	pts, err := l.scroll(ctx, within(load, kindChunk,
		match(keySource, source), match(keyChunkIndex, index)), 1)
	if err != nil {
		return recall.Citation{}, fmt.Errorf("qdrant: cite %s#%d in load %q: %w", source, index, load, err)
	}
	if len(pts) == 0 {
		return recall.Citation{}, fmt.Errorf("%w: load %q holds no chunk %d of %q — the claim that cited it "+
			"cannot be checked against this import, and must not be offered as evidence from it",
			recall.ErrNoCitation, load, index, source)
	}
	p := pts[0].Payload
	return recall.Citation{
		Source: str(p[keySource]), Index: num(p[keyChunkIndex]),
		Start: num(p[keyStart]), End: num(p[keyEnd]), Text: str(p[keyText]),
	}, nil
}

// Unanswered returns the identity questions this load carries.
//
// The duplicates, which this store keeps as their own points rather than as an
// edge between the two entities — the same decision neo4j and rdf make, for the
// same reason: a traversable "may be the same as" is a claim, and nobody has
// ruled.
func (l *Loader) Unanswered(ctx context.Context, load, about string) ([]recall.Question, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return []recall.Question{}, err
	}
	pts, err := l.scroll(ctx, within(load, kindDuplicate), 0)
	if err != nil {
		return nil, fmt.Errorf("qdrant: unanswered questions about %q in load %q: %w", about, load, err)
	}
	needle := strings.ToLower(about)
	out := []recall.Question{}
	for _, p := range pts {
		q := recall.Question{
			Signal:  alchemy.DuplicateSignal(str(p.Payload[keySignal])),
			Subject: str(p.Payload[keySubject]),
			Detail:  str(p.Payload[keyDetail]),
			Left:    sideName(p.Payload[keyLeft]),
			Right:   sideName(p.Payload[keyRight]),
		}
		// An empty about is every question, and it is empty rather than a word
		// like "all" because a sentinel that is also a legal search term is a
		// filter that silently stops filtering for one input.
		if needle == "" || strings.Contains(strings.ToLower(q.Subject+q.Detail+q.Left+q.Right), needle) {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].Detail < out[j].Detail
	})
	return out, nil
}

func node(p map[string]any) recall.Node {
	return recall.Node{ID: str(p[keyEntityID]), Type: str(p[keyType]), Name: str(p[keyName])}
}

// page orders the hits and cuts them, keeping the total.
//
// By name then id, so a limit cuts the same place twice, and the count travels
// with the page because a page that does not say it is one asks a reader to
// trust a list that is not the list.
func page(hits []recall.Node, limit int) recall.Found {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Name != hits[j].Name {
			return hits[i].Name < hits[j].Name
		}
		return hits[i].ID < hits[j].ID
	})
	found := recall.Found{Total: len(hits), Nodes: hits}
	if found.Nodes == nil {
		found.Nodes = []recall.Node{}
	}
	if len(hits) > limit {
		found.Nodes = hits[:limit]
	}
	return found
}

// Contributions reports every source that had a hand in one node.
//
// The node's own record is one contribution and the edges naming it are the
// rest, which is the same shape the other three implement and rests on the same
// fact: an edge carries its OWN provenance, so a source that never created the
// node but asserted an edge touching it is visible with its file, its chunk and
// its producer.
//
// Name is filled for the node's own record and left empty for a source
// recovered from an edge, even though this store has rel_from_name beside the
// id. That name is what the ENTITY is called, copied onto the edge when the
// edge was written, and reporting it as what the edge's source called the node
// would make every join read as unanimous — the one thing this method exists to
// stop a reader from concluding.
func (l *Loader) Contributions(ctx context.Context, load, id string) (recall.Contributions, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Contributions{}, err
	}
	if !ok {
		return recall.Contributions{}, noLoad(load)
	}
	ents, err := l.scroll(ctx, within(load, kindEntity, match(keyEntityID, id)), 1)
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("qdrant: contributions to %q in load %q: %w", id, load, err)
	}
	edgeFilter := within(load, kindRelation)
	edgeFilter["should"] = []map[string]any{match(keyRelFrom, id), match(keyRelTo, id)}
	edges, err := l.scroll(ctx, edgeFilter, 0)
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("qdrant: contributions to %q in load %q: %w", id, load, err)
	}
	if len(ents) == 0 && len(edges) == 0 {
		return recall.Contributions{}, nil
	}

	var typ string
	mentions := make([]recall.Contributor, 0, len(ents)+len(edges))
	// The node's own record first, because it is the one that carries a name
	// and the fold keeps the first non-empty Name it sees for a mention.
	for _, p := range ents {
		typ = str(p.Payload[keyType])
		prov := readProvenance(p.Payload)
		mentions = append(mentions, recall.Contributor{
			Source: prov.Source, Chunk: prov.Chunk, Producer: prov.Producer,
			Name: str(p.Payload[keyName]),
		})
	}
	for _, p := range edges {
		prov := readProvenance(p.Payload)
		mentions = append(mentions, recall.Contributor{
			Source: prov.Source, Chunk: prov.Chunk, Producer: prov.Producer,
		})
	}
	return contributions.Assemble(id, typ, mentions), nil
}

// aliasesOf decodes the list this store writes natively.
//
// It was written and never read: build.go puts Entity.Aliases on the payload
// and readEntity did not take them off again, so every alchemy.Entity this
// package returned came back with none — a field the store held and the query
// surface could not show. Found by asking for it.
func aliasesOf(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, str(i))
	}
	return out
}

// sideName reads a duplicate side's name out of the nested map build.go writes.
//
// The side is a map — id, type, name and its own provenance — because a
// duplicate is a claim about two records and each side is one of them. Reading
// it as a string returns nothing, silently, which is what it did until a test
// asked for the pair by name.
func sideName(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	return str(m[keyName])
}
