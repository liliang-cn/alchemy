package pgvector

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Types is the vocabulary of one load.
//
// GROUP BY over loaded_entities, which is a view that hides a load until its
// last statement commits -- so a load still arriving reports no vocabulary
// rather than the half of one that has landed. A count assembled from a partial
// import is the worst possible answer to "what is in this graph": it is a
// number, it is wrong, and nothing about it looks wrong.
func (l *Loader) Types(ctx context.Context, load string) ([]recall.TypeCount, error) {
	const sql = `SELECT type, count(*)::int FROM {s}.loaded_entities
	WHERE load_id = $1 GROUP BY type ORDER BY type`
	rows, err := l.pool.Query(ctx, l.q(sql), load)
	if err != nil {
		return nil, fmt.Errorf("pgvector: types in load %q: %w", load, err)
	}
	defer rows.Close()
	out := []recall.TypeCount{}
	for rows.Next() {
		var t recall.TypeCount
		if err := rows.Scan(&t.Type, &t.Count); err != nil {
			return nil, fmt.Errorf("pgvector: types in load %q: %w", load, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// OfType reads out one class.
//
// The type is compared with = rather than with the position() Find uses. Find's
// input is text somebody typed and this one's is a type an ontology declared,
// so a case fold here would report "Person" and "person" as one class the load
// does not have.
//
// The total travels with the page in one statement, as a window function rather
// than a second query, because a count taken separately is a count of a store
// that may have moved between the two.
func (l *Loader) OfType(ctx context.Context, load, typ string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("pgvector: limit = %d is not a number of entities", limit)
	}
	const sql = `SELECT entity_id, type, name, count(*) OVER ()::int AS total
	FROM {s}.loaded_entities WHERE load_id = $1 AND type = $2::text
	ORDER BY name, entity_id LIMIT $3`
	rows, err := l.pool.Query(ctx, l.q(sql), load, typ, limit)
	if err != nil {
		return recall.Found{}, fmt.Errorf("pgvector: entities of type %q in load %q: %w", typ, load, err)
	}
	defer rows.Close()
	found := recall.Found{Nodes: []recall.Node{}}
	for rows.Next() {
		var n recall.Node
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &found.Total); err != nil {
			return recall.Found{}, fmt.Errorf("pgvector: entities of type %q in load %q: %w", typ, load, err)
		}
		found.Nodes = append(found.Nodes, n)
	}
	return found, rows.Err()
}
