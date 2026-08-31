package neo4j

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"

	"github.com/liliang-cn/alchemy/connectors/internal/contributions"
)

// Contributions reports every source that had a hand in one node.
//
// WHAT A PROPERTY GRAPH CAN SEE, and it is less than the method's name
// suggests. A node here is one node: writeEntities MERGEs on (run, id), and
// pkg/sink folds the records that share an ID before this store is handed any
// of them, so what lands is the FIRST record and its provenance alone.
// pkg/preflight says what that costs in the same words when it reports
// EntityCorroborated: "a store holding one row per entity keeps one of the two
// provenances, so the second source's claim on this node is not recoverable
// from it". So the node's own provenance is exactly one contribution, and every
// other one has to be found somewhere else.
//
// Somewhere else is the edges. An edge carries its OWN provenance — that is
// what §5b buys and what writeRelations pays for by keeping two chunks that
// said the same thing as two relationships — so a source that never created the
// node but asserted an edge touching it is visible, with its file, its chunk and
// its producer. That is the whole of the Mira case: the node admits
// halcyon-profile.pdf, and its DEVELOPS edge admits team.json.
//
// WHAT IT CANNOT SEE IS THE NAME. A relationship names its endpoints by being
// attached to them, and alchemy.Relation names them by ID; neither carries what
// the asserting document called the node. So Name is filled for the node's own
// record and empty for every contribution recovered from an edge, and that
// emptiness is load-bearing. Repeating the node's `name` onto every contributor
// would cost nothing to write and would report that all of them agreed on it,
// turning the one measurement that distinguishes "joined on a full name" from
// "joined on a first name" into a constant. A reader can see that team.json had
// a hand in the node and that nothing here records what team.json called him,
// which is the true state of the graph.
//
// Bookkeeping is excluded on both ends the way the walk excludes it: the
// relationship types by name, and the nodes by label. A duplicate report or a
// retirement counted as a contribution would say a source described the node
// when what it did was raise a question about it.
func (l *Loader) Contributions(ctx context.Context, load, id string) (recall.Contributions, error) {
	stmt, err := l.contributionsCypher()
	if err != nil {
		return recall.Contributions{}, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{
		"run": load, "id": id,
		"bookkeeping": bookkeeping(), "internal": toAny(l.opts.internalLabels()),
	})
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("neo4j: contributions to %q in load %q: %w", id, load, err)
	}
	if len(recs) == 0 {
		return l.nothingContributed(ctx, load)
	}
	r := recs[0]
	// The node's own record first, because it is the one that carries a name
	// and the merge keeps the first non-empty one it sees for a mention.
	mentions := []recall.Contributor{{
		Source:   str(r["source"]),
		Chunk:    num(r["chunk"]),
		Producer: alchemy.Producer(str(r["producer"])),
		Name:     str(r["name"]),
	}}
	edges, _ := r["edges"].([]any)
	for _, e := range edges {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		mentions = append(mentions, recall.Contributor{
			Source:   str(m["source"]),
			Chunk:    num(m["chunk"]),
			Producer: alchemy.Producer(str(m["producer"])),
		})
	}
	return contributions.Assemble(id, str(r["type"]), mentions), nil
}

// contributionsCypher is the read. See findCypher for why it is a builder.
//
// One statement rather than a node query and an edge query, because a load is
// immutable once complete and two round trips would buy nothing but latency —
// and because the node's absence and the edges' absence have to be the same
// answer. OPTIONAL MATCH is what makes a node with no edges answer with one
// contribution instead of with none; an ordinary MATCH would silently report
// an isolated entity as an id the load does not hold.
//
// The CASE inside collect() is not decoration. An OPTIONAL MATCH that found
// nothing binds r to null, and a map built from a null relationship is a map of
// three nulls rather than a null — so collect() would keep it and every
// isolated node would come back carrying a contributor with no source. Reducing
// it to null first means the aggregate drops it, which is what aggregates do
// with nulls.
func (l *Loader) contributionsCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	return scope + fmt.Sprintf(
		"MATCH (n:%[1]s {%[2]s: $run, %[3]s: $id}) "+
			"WHERE NOT any(lbl IN labels(n) WHERE lbl IN $internal) "+
			"OPTIONAL MATCH (n)-[r]-(m:%[1]s {%[2]s: $run}) "+
			"WHERE NOT type(r) IN $bookkeeping AND NOT any(lbl IN labels(m) WHERE lbl IN $internal) "+
			"RETURN n.%[4]s AS type, n.%[5]s AS name, "+
			"n.%[6]s AS source, n.%[7]s AS chunk, n.%[8]s AS producer, "+
			"collect(DISTINCT CASE WHEN r IS NULL THEN null ELSE "+
			"{source: r.%[6]s, chunk: r.%[7]s, producer: r.%[8]s} END) AS edges",
		base, l.prop(keyRun), l.prop(keyID), l.prop(keyType), keyName,
		l.prop(keySource), l.prop(keyChunk), l.prop(keyProducer)), nil
}

// nothingContributed tells the two absences apart, and it is the one place in
// this file where the interface's asymmetry is spent.
//
// An id the load does not hold is an ordinary answer — nothing contributed to a
// node that is not there — and a load that is not here is the caller naming the
// wrong import, which is the bug the load parameter exists for arriving as a
// typo instead of as a silent wrong answer. It costs a second query and only on
// the empty path, so the ordinary read pays nothing for it.
func (l *Loader) nothingContributed(ctx context.Context, load string) (recall.Contributions, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Contributions{}, err
	}
	if !ok {
		return recall.Contributions{}, fmt.Errorf("%w: %q is not a finished load in this graph; "+
			"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
			recall.ErrNoLoad, load)
	}
	return recall.Contributions{}, nil
}
