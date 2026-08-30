package neo4j

import (
	"context"
	"fmt"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// group is a run of records that share a label or a relationship type.
//
// Grouping exists because Cypher cannot parameterise a label: one statement
// can carry a thousand rows only if all thousand go to the same label. It is
// also the reason quoteIdent is called once per group rather than once per
// record — one concatenation site, and it is small enough to read.
type group struct {
	key string
	idx []int
}

// groupBy keeps first-appearance order rather than sorting. A load that writes
// records in the order the result listed them is one an operator can follow
// against the JSON when it fails at batch nine.
func groupBy(idx []int, keyOf func(int) string) []group {
	var out []group
	at := map[string]int{}
	for _, i := range idx {
		k := keyOf(i)
		g, ok := at[k]
		if !ok {
			at[k] = len(out)
			out = append(out, group{key: k})
			g = len(out) - 1
		}
		out[g].idx = append(out[g].idx, i)
	}
	return out
}

// batches cuts a group into transaction-sized pieces. §8.4: a large result
// does not fit in one transaction, and there is only one code path so that the
// large case is not the untested one.
func batches(idx []int, size int) [][]int {
	var out [][]int
	for len(idx) > 0 {
		n := min(size, len(idx))
		out = append(out, idx[:n])
		idx = idx[n:]
	}
	return out
}

// labels renders the base label and one ontology type. The base label is on
// every node so that "everything alchemy imported" is a label rather than a
// guess; a type that happens to equal the base label is written once, since
// `:A:A` is a statement about one label pretending to be two.
func (l *Loader) labels(typ string) (string, error) {
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return "", fmt.Errorf("base label: %w", err)
	}
	if typ == l.opts.BaseLabel {
		return base, nil
	}
	t, err := quoteIdent(typ)
	if err != nil {
		return "", err
	}
	return base + ":" + t, nil
}

// writeEntities loads the nodes.
//
// The MERGE key is (run, id) and never id alone; Options.RunID carries the
// argument. Properties are set with `+=` from a parameter map, so no property
// name and no value is ever concatenated into the statement — the only
// interpolated things in this package are the labels, through quoteIdent.
func (l *Loader) writeEntities(ctx context.Context, p *plan, rep *Report) error {
	pre := l.opts.ReservedPrefix
	for _, g := range groupBy(p.entities, func(i int) string { return p.res.Entities[i].Type }) {
		lbl, err := l.labels(g.key)
		if err != nil {
			return err
		}
		stmt := fmt.Sprintf("UNWIND $rows AS row MERGE (n:%s {`%s%s`: $run, `%s%s`: row.id}) SET n += row.props",
			lbl, pre, keyRun, pre, keyID)
		for _, b := range batches(g.idx, l.opts.BatchSize) {
			rows := make([]any, 0, len(b))
			for _, i := range b {
				e := p.res.Entities[i]
				props, encoded, err := attributeProps(e.Attributes, pre)
				if err != nil {
					return fmt.Errorf("entity %s: %w", e.ID, err)
				}
				// Alchemy's own fields go on last, so a name attribute that
				// agreed with Entity.Name (preflight allows exactly that case)
				// cannot disagree by the time it is written.
				props["name"] = e.Name
				props[pre+keyType] = e.Type
				if len(encoded) > 0 {
					props[pre+keyJSONAttrs] = toAny(encoded)
				}
				for k, v := range provenanceProps(e.Provenance, pre) {
					props[k] = v
				}
				rows = append(rows, map[string]any{"id": e.ID, "props": props})
			}
			if err := l.runBatch(ctx, stmt, rows); err != nil {
				return fmt.Errorf("entities of type %q: %w", g.key, err)
			}
			rep.Batches++
			rep.Entities += len(b)
		}
	}
	return nil
}

// writeRelations loads the edges, after the nodes, matching both endpoints
// within the run.
//
// The MERGE key is the assertion — see relationKey. Two chunks that both said
// the same thing stay two edges, because §5b's promise is that each of them
// can name its own producer and a merged edge can only name one.
func (l *Loader) writeRelations(ctx context.Context, p *plan, rep *Report) error {
	pre := l.opts.ReservedPrefix
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return err
	}
	for _, g := range groupBy(p.relations, func(i int) string { return p.res.Relations[i].Type }) {
		typ, err := quoteIdent(g.key)
		if err != nil {
			return err
		}
		stmt := fmt.Sprintf(
			"UNWIND $rows AS row "+
				"MATCH (a:%[1]s {`%[2]s%[3]s`: $run, `%[2]s%[4]s`: row.from}) "+
				"MATCH (b:%[1]s {`%[2]s%[3]s`: $run, `%[2]s%[4]s`: row.to}) "+
				"MERGE (a)-[r:%[5]s {`%[2]s%[6]s`: row.key}]->(b) SET r += row.props",
			base, pre, keyRun, keyID, typ, keyEdgeKey)
		for _, b := range batches(g.idx, l.opts.BatchSize) {
			rows := make([]any, 0, len(b))
			for _, i := range b {
				r := p.res.Relations[i]
				props, encoded, err := attributeProps(r.Attributes, pre)
				if err != nil {
					return fmt.Errorf("relation %s-[%s]->%s: %w", r.From, r.Type, r.To, err)
				}
				props[pre+keyType] = r.Type
				props[pre+keyRun] = l.opts.RunID
				if len(encoded) > 0 {
					props[pre+keyJSONAttrs] = toAny(encoded)
				}
				for k, v := range provenanceProps(r.Provenance, pre) {
					props[k] = v
				}
				rows = append(rows, map[string]any{"from": r.From, "to": r.To, "key": relationKey(r), "props": props})
			}
			if err := l.runBatch(ctx, stmt, rows); err != nil {
				return fmt.Errorf("relations of type %q: %w", g.key, err)
			}
			rep.Batches++
			rep.Relations += len(b)
		}
	}
	return nil
}

// runBatch is one transaction. It is the only place a batch is sent, so the
// answer to "how much work does a failure lose?" is one number, and it is
// Options.BatchSize.
func (l *Loader) runBatch(ctx context.Context, stmt string, rows []any) error {
	if len(rows) == 0 {
		return nil
	}
	return l.write(ctx, func(tx driver.ManagedTransaction) error {
		_, err := tx.Run(ctx, stmt, map[string]any{"rows": rows, "run": l.opts.RunID})
		return err
	})
}

// toAny widens a []string for the driver, which carries lists as []any.
func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
