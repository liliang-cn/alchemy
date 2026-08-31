package pgvector

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/recall"

	"github.com/liliang-cn/alchemy/connectors/internal/contributions"
)

// Contributions reports every source that had a hand in one node.
//
// WHAT THIS SCHEMA CAN SEE. entities is keyed (load_id, entity_id), so a node
// is one row carrying one prov_source, one prov_chunk and one prov_producer.
// pkg/preflight says what that costs in the same words when it reports
// EntityCorroborated: "a store holding one row per entity keeps one of the two
// provenances, so the second source's claim on this node is not recoverable
// from it". This connector never even gets that far — checkEntityIDs refuses
// two entities under one ID before a row exists, agreeing or not — so the
// node's own record is exactly one contribution here and always will be.
//
// The other contributions are in relations, where prov_* is a column on the
// edge rather than on its subject. A source that never created the node but
// asserted an edge naming it is visible with its file, its chunk and its
// producer, which is the whole of the case this method was written for: the
// node admits halcyon-profile.pdf, and its DEVELOPS row admits team.json.
//
// WHAT IT CANNOT SEE IS THE NAME. relations holds from_id and to_id and no
// endpoint name, because alchemy.Relation names entities by ID. So Name is
// filled for the node's own row and empty for every contribution recovered from
// an edge. Filling it from the entity row instead would be one join away and
// would report that every source called the node what the entity row calls it —
// making every join unanimous, which is precisely the false confidence
// recall.Contributor exists to end.
//
// AN ID WITH EDGES AND NO ROW IS ANSWERED, and this store is the only one of
// the three where that case exists. There is no foreign key from a relation to
// an entity, deliberately, because a relation naming an entity the result did
// not contain is ViolationDanglingRelation and §7.3 delivers it with the graph.
// Claims already renders such an edge — "a claim about something this load does
// not describe, which is different from no claim" — and this answers it the
// same way: the sources that referred to the node, with an empty Type, rather
// than a zero Contributions that would say nobody mentioned it. The graph
// stores drop those edges at write time and cannot reach this case at all.
func (l *Loader) Contributions(ctx context.Context, load, id string) (recall.Contributions, error) {
	// One statement, and the node's own row first: it is the only row that
	// carries a name, and the merge keeps the first non-empty Name it sees for
	// a mention. The relation side is reduced to its distinct provenances
	// inside the database rather than in Go, because two edges extracted from
	// one sentence are one source having a hand in the node once, and a reader
	// counting rows would otherwise read a single sentence as a corroboration.
	const sql = `SELECT e.type, e.name, e.prov_source, e.prov_chunk, e.prov_producer, true AS own
	FROM {s}.loaded_entities e WHERE e.load_id = $1 AND e.entity_id = $2::text
UNION ALL
SELECT ''::text, ''::text, r.prov_source, r.prov_chunk, r.prov_producer, false
	FROM (SELECT DISTINCT prov_source, prov_chunk, prov_producer FROM {s}.loaded_relations
		WHERE load_id = $1 AND (from_id = $2::text OR to_id = $2::text)) r
ORDER BY 6 DESC, 3, 4, 5`
	rows, err := l.pool.Query(ctx, l.q(sql), load, id)
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("pgvector: contributions to %q in load %q: %w", id, load, err)
	}
	defer rows.Close()
	var typ string
	var mentions []recall.Contributor
	for rows.Next() {
		var rowType, name string
		var c recall.Contributor
		var own bool
		if err := rows.Scan(&rowType, &name, &c.Source, &c.Chunk, &c.Producer, &own); err != nil {
			return recall.Contributions{}, fmt.Errorf("pgvector: contributions to %q in load %q: %w", id, load, err)
		}
		if own {
			typ, c.Name = rowType, name
		}
		mentions = append(mentions, c)
	}
	if err := rows.Err(); err != nil {
		return recall.Contributions{}, fmt.Errorf("pgvector: contributions to %q in load %q: %w", id, load, err)
	}
	if len(mentions) == 0 {
		return l.nothingContributed(ctx, load)
	}
	return contributions.Assemble(id, typ, mentions), nil
}

// nothingContributed tells the two absences apart, and it is where the
// interface's asymmetry is spent.
//
// An id the load does not hold is an ordinary answer — nothing contributed to a
// node that is not there — and a load that is not here is a caller naming the
// wrong import, which is the bug the load parameter exists for arriving as a
// typo rather than as a wrong answer. The loaded_* views hide a load that has
// not committed its last statement, so an unfinished load and an absent one are
// the same answer here and are reported as the same thing. It costs a query on
// the empty path only, so the ordinary read pays nothing for it.
func (l *Loader) nothingContributed(ctx context.Context, load string) (recall.Contributions, error) {
	var n int
	err := l.pool.QueryRow(ctx, l.q(`SELECT count(*) FROM {s}.loads WHERE id = $1 AND state = '`+stateComplete+`'`), load).Scan(&n)
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("pgvector: reading load %q: %w", load, err)
	}
	if n == 0 {
		return recall.Contributions{}, fmt.Errorf("%w: %q is not a finished load in this schema; "+
			"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
			recall.ErrNoLoad, load)
	}
	return recall.Contributions{}, nil
}
