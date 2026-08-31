package neo4j

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Types is the vocabulary of one load.
//
// The count is COUNT of nodes carrying the type and not of anything else, which
// is worth saying because this store keeps its own records under the same base
// label: a chunk, a duplicate report and the run marker are all nodes in this
// graph, and a count that included them would report a vocabulary the ontology
// never declared. They are excluded by label, the way every read here excludes
// them, and the label test is the guard the type test needs -- a bookkeeping
// node has no `_type` today, and that is a property of the writer rather than a
// rule anything holds still.
func (l *Loader) Types(ctx context.Context, load string) ([]recall.TypeCount, error) {
	stmt, err := l.typesCypher()
	if err != nil {
		return nil, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{
		"run": load, "internal": toAny(l.opts.internalLabels()),
	})
	if err != nil {
		return nil, fmt.Errorf("neo4j: types in load %q: %w", load, err)
	}
	out := make([]recall.TypeCount, 0, len(recs))
	for _, r := range recs {
		out = append(out, recall.TypeCount{Type: str(r["type"]), Count: num(r["n"])})
	}
	return out, nil
}

func (l *Loader) typesCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	return scope + fmt.Sprintf(
		"MATCH (n:%[1]s {%[2]s: $run}) "+
			"WHERE n.%[3]s IS NOT NULL AND NOT any(lbl IN labels(n) WHERE lbl IN $internal) "+
			"RETURN n.%[3]s AS type, count(n) AS n ORDER BY type",
		base, l.prop(keyRun), l.prop(keyType)), nil
}

// OfType reads out one class.
//
// The type is compared exactly rather than folded, because a type is declared
// by an ontology and Find's case-insensitive rule is for text somebody typed.
// The page and its total are collected in one statement for the reason
// findCypher gives: a second query would count a store that had moved.
func (l *Loader) OfType(ctx context.Context, load, typ string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("neo4j: limit = %d is not a number of entities", limit)
	}
	stmt, err := l.ofTypeCypher()
	if err != nil {
		return recall.Found{}, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{
		"run": load, "type": typ, "limit": int64(limit),
		"internal": toAny(l.opts.internalLabels()),
	})
	if err != nil {
		return recall.Found{}, fmt.Errorf("neo4j: entities of type %q in load %q: %w", typ, load, err)
	}
	return pageOf(recs), nil
}

func (l *Loader) ofTypeCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	return scope + fmt.Sprintf(
		"MATCH (n:%[1]s {%[2]s: $run, %[3]s: $type}) "+
			"WHERE NOT any(lbl IN labels(n) WHERE lbl IN $internal) "+
			"WITH DISTINCT n.%[4]s AS id, n.%[3]s AS type, n.%[5]s AS name "+
			"ORDER BY name, id "+
			"WITH collect({id: id, type: type, name: name}) AS matches "+
			"RETURN size(matches) AS total, matches[0..$limit] AS page",
		base, l.prop(keyRun), l.prop(keyType), l.prop(keyID), keyName), nil
}
