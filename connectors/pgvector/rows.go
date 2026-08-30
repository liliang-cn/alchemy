package pgvector

import (
	"context"
	"encoding/json"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// provNames is the provenance projection as a column list, spelled once and
// used by every table that carries provenance, so a field added to
// alchemy.Provenance shows up as one compile error rather than three tables
// that quietly disagree about what they store.
var provNames = []string{
	"prov_source", "prov_chunk", "prov_producer", "prov_deterministic", "prov_model",
	"prov_ontology", "prov_chunking", "prov_confidence", "prov_reviewed_by", "prov_rule_set", "prov_ruled_by",
}

// provRow renders one Provenance in provNames order.
//
// prov_deterministic is computed here, by calling Producer.Deterministic(),
// rather than derived in SQL. That method is the single statement of the rule
// and it lives in the core module; a CASE expression in this connector's DDL
// would be a second one, and the two would disagree the first time a producer
// was added — which is exactly the direction Deterministic()'s own comment
// says it must be safe in.
func provRow(p alchemy.Provenance) []any {
	return []any{
		p.Source, p.Chunk, string(p.Producer), p.Producer.Deterministic(), p.Model,
		p.Ontology, p.Chunking, p.Confidence, p.ReviewedBy, p.RuleSet, p.RuledBy,
	}
}

// attrs renders an attribute map as jsonb, or NULL when there was none.
//
// The distinction is kept rather than flattened to '{}' because "the source
// stated nothing beyond type and name" and "the source stated an empty object"
// are different facts, and a store that renders both as '{}' has decided one of
// them never happened.
func attrs(m map[string]any) (any, error) {
	if m == nil {
		return nil, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// with appends the provenance part of a row (or of a column list) to the part
// that is particular to the table. It is generic so the columns and the values
// are built by the same call in the same order — the ordering of a COPY row
// against its column list is the one mistake in this file that would produce
// data that loads cleanly and means something else.
func with[T any](head, tail []T) []T { return append(append([]T{}, head...), tail...) }

// write puts every table of one result into the store, largest last.
//
// The order is not arbitrary: chunks and their embeddings are what a search
// reads, entities and relations are what a search's neighbourhood reads, and
// the findings are read by a person. Nothing here is visible until complete()
// runs, so the order only decides what a failure has already cost, not what
// anybody can see.
func (l *Loader) write(ctx context.Context, id string, res alchemy.Result, dim int) error {
	if err := l.writeChunks(ctx, id, res, dim); err != nil {
		return err
	}
	if err := l.writeEntities(ctx, id, res); err != nil {
		return err
	}
	if err := l.writeRelations(ctx, id, res); err != nil {
		return err
	}
	if err := l.writeViolations(ctx, id, res); err != nil {
		return err
	}
	return l.writeDuplicates(ctx, id, res)
}

func (l *Loader) writeChunks(ctx context.Context, id string, res alchemy.Result, dim int) error {
	cols := []string{"load_id", "idx", "source", "strategy", "heading", "start_byte", "end_byte", "body", "embed_model"}
	// The embedding column only exists once a dimension has been bound, so a
	// result with no vectors writes a narrower row rather than a NULL into a
	// column that is not there.
	byChunk := make(map[int]alchemy.Vector, len(res.Vectors))
	for _, v := range res.Vectors {
		byChunk[v.Chunk] = v
	}
	if dim > 0 {
		cols = append(cols, "embedding")
	}
	return l.copyRows(ctx, "chunks", cols, len(res.Chunks), func(i int) ([]any, error) {
		c := res.Chunks[i]
		row := []any{id, c.Index, c.Source, c.Strategy, c.Heading, c.Start, c.End, c.Text, ""}
		if dim == 0 {
			return row, nil
		}
		v, ok := byChunk[c.Index]
		if !ok {
			// A chunk nobody embedded. §5c puts the embedding after review, so
			// a chunk that was rejected or that produced nothing legitimately
			// has no vector; it keeps its text and is simply not searchable by
			// similarity.
			return append(row, nil), nil
		}
		row[len(row)-1] = v.Model
		return append(row, v.Values), nil
	})
}

func (l *Loader) writeEntities(ctx context.Context, id string, res alchemy.Result) error {
	cols := with([]string{"load_id", "entity_id", "type", "name", "attributes"}, provNames)
	return l.copyRows(ctx, "entities", cols, len(res.Entities), func(i int) ([]any, error) {
		e := res.Entities[i]
		a, err := attrs(e.Attributes)
		if err != nil {
			return nil, err
		}
		return with([]any{id, e.ID, e.Type, e.Name, a}, provRow(e.Provenance)), nil
	})
}

func (l *Loader) writeRelations(ctx context.Context, id string, res alchemy.Result) error {
	cols := with([]string{"load_id", "seq", "from_id", "to_id", "type", "attributes"}, provNames)
	return l.copyRows(ctx, "relations", cols, len(res.Relations), func(i int) ([]any, error) {
		r := res.Relations[i]
		a, err := attrs(r.Attributes)
		if err != nil {
			return nil, err
		}
		return with([]any{id, i, r.From, r.To, r.Type, a}, provRow(r.Provenance)), nil
	})
}

func (l *Loader) writeViolations(ctx context.Context, id string, res alchemy.Result) error {
	cols := with([]string{"load_id", "seq", "kind", "detail", "subject"}, provNames)
	return l.copyRows(ctx, "violations", cols, len(res.Violations), func(i int) ([]any, error) {
		v := res.Violations[i]
		return with([]any{id, i, string(v.Kind), v.Detail, v.Subject}, provRow(v.Provenance)), nil
	})
}

func (l *Loader) writeDuplicates(ctx context.Context, id string, res alchemy.Result) error {
	cols := []string{"load_id", "seq", "signal", "subject", "detail",
		"left_id", "left_type", "left_name", "left_prov",
		"right_id", "right_type", "right_name", "right_prov"}
	return l.copyRows(ctx, "duplicates", cols, len(res.Duplicates), func(i int) ([]any, error) {
		d := res.Duplicates[i]
		lp, err := json.Marshal(d.Left.Provenance)
		if err != nil {
			return nil, err
		}
		rp, err := json.Marshal(d.Right.Provenance)
		if err != nil {
			return nil, err
		}
		return []any{id, i, string(d.Signal), d.Subject, d.Detail,
			d.Left.ID, d.Left.Type, d.Left.Name, json.RawMessage(lp),
			d.Right.ID, d.Right.Type, d.Right.Name, json.RawMessage(rp)}, nil
	})
}
