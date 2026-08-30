package pgvector

import (
	"context"
	"encoding/json"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// provNames is the provenance projection as a column list, spelled once and
// used by every table that carries provenance, so a field added to
// alchemy.Provenance shows up as one compile error rather than three tables
// that quietly disagree about what they store.
var provNames = []string{
	"prov_source", "prov_chunk", "prov_producer", "prov_deterministic", "prov_model",
	"prov_ontology", "prov_chunking", "prov_confidence", "prov_reviewed_by", "prov_rule_set", "prov_ruled_by",
	// The asserter and the date, for alchemy.ProducerHuman.
	"prov_by", "prov_at",
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
		// The asserter and the date, for alchemy.ProducerHuman: the only thing
		// that tells a fact a named person stated from one a file happened to
		// contain.
		p.By, p.At,
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

// The writers below take one batch and the offset it starts at, rather than a
// whole result, because that is what the envelope hands them (pkg/sink): a
// large graph reaches a store as a stream of batches and never as a struct, so
// a writer that took a Result would put §8.4's whole four-hundred-thousand-
// record import in this process's heap in order to COPY it out again.
//
// The offset is what `seq` is: the record's position in the load, counted
// across batches. It was the position in the slice, which was the same number
// only because the slice was the whole result.

func (l *Loader) writeChunkBatch(ctx context.Context, id string, dim int, batch []sink.Chunk) error {
	cols := []string{"load_id", "idx", "source", "strategy", "heading", "start_byte", "end_byte", "body", "embed_model"}
	// The embedding column only exists once a dimension has been bound, so a
	// result with no vectors writes a narrower row rather than a NULL into a
	// column that is not there.
	if dim > 0 {
		cols = append(cols, "embedding")
	}
	return l.copyRows(ctx, "chunks", cols, len(batch), func(i int) ([]any, error) {
		c := batch[i]
		row := []any{id, c.Index, c.Source, c.Strategy, c.Heading, c.Start, c.End, c.Text, c.Model}
		if dim == 0 {
			return row, nil
		}
		// A chunk nobody embedded. §5c puts the embedding after review, so a
		// chunk that was rejected or that produced nothing legitimately has no
		// vector; it keeps its text and is simply not searchable by similarity.
		// The envelope carries the two together, so this is a nil field rather
		// than a lookup that could miss.
		if c.Vector == nil {
			return append(row, nil), nil
		}
		return append(row, c.Vector), nil
	})
}

func (l *Loader) writeEntityBatch(ctx context.Context, id string, batch []alchemy.Entity) error {
	cols := with([]string{"load_id", "entity_id", "type", "name", "attributes"}, provNames)
	return l.copyRows(ctx, "entities", cols, len(batch), func(i int) ([]any, error) {
		e := batch[i]
		a, err := attrs(e.Attributes)
		if err != nil {
			return nil, err
		}
		return with([]any{id, e.ID, e.Type, e.Name, a}, provRow(e.Provenance)), nil
	})
}

func (l *Loader) writeRelationBatch(ctx context.Context, id string, at int, batch []alchemy.Relation) error {
	cols := with([]string{"load_id", "seq", "from_id", "to_id", "type", "attributes"}, provNames)
	return l.copyRows(ctx, "relations", cols, len(batch), func(i int) ([]any, error) {
		r := batch[i]
		a, err := attrs(r.Attributes)
		if err != nil {
			return nil, err
		}
		return with([]any{id, at + i, r.From, r.To, r.Type, a}, provRow(r.Provenance)), nil
	})
}

func (l *Loader) writeViolationBatch(ctx context.Context, id string, batch []alchemy.Violation) error {
	cols := with([]string{"load_id", "seq", "kind", "detail", "subject"}, provNames)
	return l.copyRows(ctx, "violations", cols, len(batch), func(i int) ([]any, error) {
		v := batch[i]
		return with([]any{id, i, string(v.Kind), v.Detail, v.Subject}, provRow(v.Provenance)), nil
	})
}

// supersessionCols and supersessionRow are the two halves of one list, kept
// beside each other for the reason the four provenance lists were not and cost
// a production write for it: a column list and the row it names are checked
// against each other by the database and by nothing in Go.
//
// The whole Ref is stored and not only its id. A supersession may retire a
// relation, and a Ref naming an edge carries the four fields Relation.Identity
// is a function of — so a reader holding this row can work out what replaces
// the retired record without going back to the result it came in.
var supersessionCols = []string{"load_id", "seq", "retires", "reason",
	"by_kind", "by_id", "by_type", "by_from", "by_to", "by_key"}

func supersessionRow(id string, i int, s alchemy.Supersession) []any {
	return []any{id, i, s.Retires, s.Reason,
		string(s.By.Kind), s.By.ID, s.By.Type, s.By.From, s.By.To, s.By.Key}
}

// writeSupersessionBatch records what the result says is over. It records it
// and does not perform it: no row is deleted, no load is retracted, and the
// entity or relation named in `retires` is exactly as it was. alchemy states a
// retirement and never acts on one, and a connector is the step at which acting
// would stop being a claim in a job and become a row missing from a table
// somebody queries.
//
// There is no foreign key from `retires` to entities, for a stronger version of
// the reason the relations table has none: Supersession.Retires "deliberately
// need not be present in this result" — the record being retired is usually in
// the store from a load that finished last month, or under a name this database
// has never seen. A foreign key would refuse the correction for naming exactly
// the thing it exists to name.
func (l *Loader) writeSupersessionBatch(ctx context.Context, id string, at int, batch []alchemy.Supersession) error {
	cols := with(supersessionCols, provNames)
	return l.copyRows(ctx, "supersessions", cols, len(batch), func(i int) ([]any, error) {
		s := batch[i]
		// The supersession's own provenance rather than the superseding
		// record's: a reviewer may retire a record a model proposed, and those
		// are two claims by two parties.
		return with(supersessionRow(id, at+i, s), provRow(s.Provenance)), nil
	})
}

func (l *Loader) writeDuplicateBatch(ctx context.Context, id string, batch []alchemy.Duplicate) error {
	cols := []string{"load_id", "seq", "signal", "subject", "detail",
		"left_id", "left_type", "left_name", "left_prov",
		"right_id", "right_type", "right_name", "right_prov"}
	return l.copyRows(ctx, "duplicates", cols, len(batch), func(i int) ([]any, error) {
		d := batch[i]
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
