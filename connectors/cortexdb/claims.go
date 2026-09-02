package cortexdb

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/recall"
	cgraph "github.com/liliang-cn/cortexdb/v2/pkg/graph"

	"github.com/liliang-cn/alchemy/connectors/internal/contributions"
)

// adjacent reads the edges touching one entity of one load, in both
// directions, and drops everything that is not a claim.
//
// Three kinds of edge reach this node and only one of them is an assertion
// about the world. CortexDB writes a mention edge from every chunk that names
// an entity, which says the text mentioned it and not that anything is true of
// it; the store may also hold edges some other tool wrote — this is a shared
// brain, and a memory that links two people is not this load's claim about
// them. Both are excluded by the same structural test: an alchemy claim runs
// between two nodes this connector minted, for THIS run, and entityNodeID puts
// all three facts in the id.
//
// Testing the id rather than a property is the one place this file reads the
// id shape, and it is the right test here for the reason the property is right
// in scope(): the question is not "which batch is this edge in" but "are both
// of its ends entities of the batch I am reading" — which is a statement about
// the two node ids, and asking it of the nodes would be two more round trips
// per edge to learn something their names already say.
func (l *Loader) adjacent(ctx context.Context, load, id string) ([]*cgraph.GraphEdge, error) {
	edges, err := l.cortex.Graph().GetEdges(ctx, entityNodeID(load, id), "both")
	if err != nil {
		return nil, err
	}
	want := entityNodeID(load, "")
	out := edges[:0]
	for _, e := range edges {
		if strings.HasPrefix(e.FromNodeID, want) && strings.HasPrefix(e.ToNodeID, want) {
			out = append(out, e)
		}
	}
	return out, nil
}

// names resolves entity node ids to what the load calls them, in one read.
//
// One read and not one per endpoint: a well-connected node has hundreds of
// edges, and a walk that fetched each end separately would turn one question
// into hundreds of round trips — the cost recall.Claim's FromID and ToID exist
// to save a caller, spent inside the connector instead.
func (l *Loader) names(ctx context.Context, ids []string) (map[string]recall.Endpoint, error) {
	if len(ids) == 0 {
		return map[string]recall.Endpoint{}, nil
	}
	nodes, err := l.cortex.Graph().GetNodesBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]recall.Endpoint, len(nodes))
	for _, n := range nodes {
		out[n.ID] = recall.Endpoint{
			ID:   prop(n.Properties, l.opts.ReservedPrefix+keyEntityID),
			Name: prop(n.Properties, "name"),
		}
	}
	return out, nil
}

// Claims returns every claim adjacent to one entity, each carrying its own
// provenance.
//
// A fused edge answers as the several claims it is. CortexDB keys an edge on
// (from, to, type, document), so two chunks of one file that both said the
// same thing become one edge here where Neo4j would hold two — and
// writeRelations stores every member's provenance as an array against exactly
// this read. Answering with the edge's flat fields would report one assertion
// where the corpus made two, which is the corroboration §5b spends its length
// on, deleted at the last step.
func (l *Loader) Claims(ctx context.Context, load, id string) ([]recall.Claim, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return nil, err
	}
	edges, err := l.adjacent(ctx, load, id)
	if err != nil {
		return nil, fmt.Errorf("cortexdb: claims about %q in load %q: %w", id, load, err)
	}
	ends := map[string]bool{}
	for _, e := range edges {
		ends[e.FromNodeID] = true
		ends[e.ToNodeID] = true
	}
	ids := make([]string, 0, len(ends))
	for k := range ends {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	named, err := l.names(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("cortexdb: endpoints of %q in load %q: %w", id, load, err)
	}

	var out []recall.Claim
	for _, e := range edges {
		for _, p := range provenancesOf(e.Properties, l.opts.ReservedPrefix) {
			out = append(out, recall.NewClaim(named[e.FromNodeID], named[e.ToNodeID], e.EdgeType, p))
		}
	}
	// Ordered by type, then object, then subject, then source and chunk — the
	// interface's order, so two reads of one node produce the same document
	// and a diff of two loads is about the graph rather than about row order.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Type != b.Type:
			return a.Type < b.Type
		case a.To != b.To:
			return a.To < b.To
		case a.From != b.From:
			return a.From < b.From
		case a.Source != b.Source:
			return a.Source < b.Source
		default:
			return a.Chunk < b.Chunk
		}
	})
	return out, nil
}

// Contributions reports every source that had a hand in one node.
//
// WHAT THIS STORE CAN SEE, and it is the same shape the other three report for
// the same reason. A node here is one node: pkg/sink folds the records sharing
// an ID before this connector is handed any of them, and pkg/preflight states
// the cost in the words this connector repeats — "a store holding one row per
// entity keeps one of the two provenances, so the second source's claim on
// this node is not recoverable from it". So the node's own provenance is
// exactly one contribution and every other one comes off an edge.
//
// The edges are where this store is unusually good at it. Every member of a
// fused edge kept its own provenance in the array, so two chunks that both
// asserted one relation are two contributions here — where the flat fields
// would have reported one, and a reader would have seen a single source behind
// a fact that two of them stated.
//
// WHAT IT CANNOT SEE IS THE NAME. An edge names its endpoints by node id, and
// nothing on it records what the asserting document called them. So Name is
// filled for the node's own record and empty for every contribution recovered
// from an edge, and the emptiness is the measurement: copying the node's name
// onto every contributor would report that all of them agreed on it, turning
// the one signal that distinguishes "joined on a full name" from "joined on a
// first name" into a constant.
func (l *Loader) Contributions(ctx context.Context, load, id string) (recall.Contributions, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Contributions{}, err
	}
	if !ok {
		return recall.Contributions{}, noLoad(load)
	}
	node, err := l.cortex.Graph().GetNode(ctx, entityNodeID(load, id))
	if err != nil || node == nil {
		// An id the load does not hold is an ordinary answer — nothing
		// contributed to a node that is not there. The load's absence was
		// already refused above, which is the asymmetry the interface draws.
		return recall.Contributions{}, nil
	}
	pre := l.opts.ReservedPrefix
	own := provenanceFrom(node.Properties, pre)
	mentions := []recall.Contributor{{
		Source: own.Source, Chunk: own.Chunk, Producer: own.Producer,
		Name: prop(node.Properties, "name"),
	}}
	edges, err := l.adjacent(ctx, load, id)
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("cortexdb: contributions to %q in load %q: %w", id, load, err)
	}
	for _, e := range edges {
		for _, p := range provenancesOf(e.Properties, pre) {
			mentions = append(mentions, recall.Contributor{
				Source: p.Source, Chunk: p.Chunk, Producer: p.Producer,
			})
		}
	}
	return contributions.Assemble(id, prop(node.Properties, pre+keyDeclaredType), mentions), nil
}
