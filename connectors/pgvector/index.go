package pgvector

import (
	"context"
	"fmt"
	"math"
)

// IndexKind is which of pgvector's two index types to build.
type IndexKind string

const (
	// IndexHNSW is the default, and the default because it needs no training
	// data. An index is built at some moment in a corpus's life, and with
	// ivfflat that moment decides the centroids: build it after a tenth of the
	// corpus is in and every later query is answered by lists that were fitted
	// to a tenth of the data. HNSW has no such moment.
	IndexHNSW IndexKind = "hnsw"
	// IndexIVFFlat is offered for the buyer who has measured and wants the
	// smaller, faster-to-build structure. It is not the default because the
	// failure mode of a badly-fitted ivfflat is silent: it returns fewer and
	// worse neighbours, at full speed, with no error anywhere.
	IndexIVFFlat IndexKind = "ivfflat"
)

// DefaultIndexMinRows is the size below which EnsureVectorIndex declines.
//
// The number is a judgement and is meant to be overridden, but the direction is
// not: an index over a few thousand rows is slower than the sequential scan it
// replaces, because the scan is one pass over a table that fits in cache and
// the index is a graph walk plus a heap fetch per candidate. It also costs
// build time and memory forever afterwards, and for ivfflat it fits centroids
// to a sample that is not the corpus. So the connector's answer to a small
// table is no, with the numbers attached.
const DefaultIndexMinRows = 10000

// IndexOptions is what a caller can say about the index.
type IndexOptions struct {
	// Kind defaults to IndexHNSW.
	Kind IndexKind
	// MinRows overrides DefaultIndexMinRows. Setting it to 1 means "build it
	// anyway", which is a thing a caller is allowed to mean.
	MinRows int
	// Lists is ivfflat's list count. Zero derives it from the row count.
	Lists int
	// M and EFConstruction are HNSW's build parameters. Zero leaves pgvector's
	// defaults, which are the ones its own documentation's numbers were
	// measured with.
	M, EFConstruction int
}

// IndexReport says what was decided and why. The reason is the product: a
// caller who gets no index is owed the number that decided it, or they will
// conclude the connector is broken rather than that their table is small.
type IndexReport struct {
	Created bool
	Name    string
	Kind    IndexKind
	Rows    int
	Lists   int
	Reason  string
}

// EnsureVectorIndex builds the vector index, or explains why it did not.
//
// It is a separate call from Load, deliberately. §8.4's world is one where a
// corpus arrives over many loads, and an index built after the first of them is
// an index maintained through all the rest — every subsequent COPY paying HNSW
// insertion cost per row. The order that works for a large import is the one
// every bulk-load guide gives and this API makes possible: load everything,
// then build once.
//
// What a buyer with ten million chunks should do differently is stated here
// rather than in a README, because it is a property of this method:
//
//   - Build after the last load, not the first, for the reason above.
//   - Raise maintenance_work_mem for the session that builds it. HNSW builds
//     inside that budget and spills to a much slower path when it does not fit;
//     the difference is hours.
//   - Raise max_parallel_maintenance_workers. HNSW builds in parallel and the
//     default of two leaves most of a machine idle.
//   - If the store is serving queries while the index is built, build it
//     CONCURRENTLY by hand — this method does not, because CONCURRENTLY cannot
//     run inside a transaction and a failed concurrent build leaves an invalid
//     index that silently answers nothing.
//   - Consider ivfflat if build time matters more than recall, and then
//     measure probes: the default of one probe over sqrt(n) lists is a recall
//     number, not a performance one.
func (l *Loader) EnsureVectorIndex(ctx context.Context, opts IndexOptions) (IndexReport, error) {
	dim, err := l.boundDimension(ctx)
	if err != nil {
		return IndexReport{}, err
	}
	if dim == 0 {
		return IndexReport{}, fmt.Errorf("pgvector: this schema has no vector column yet, so there is nothing to index; " +
			"load a result that carries vectors first")
	}
	kind := opts.Kind
	if kind == "" {
		kind = IndexHNSW
	}
	if kind != IndexHNSW && kind != IndexIVFFlat {
		return IndexReport{}, fmt.Errorf("pgvector: %q is not an index kind; use hnsw or ivfflat", kind)
	}
	opClass, _, err := l.dist.opClass()
	if err != nil {
		return IndexReport{}, err
	}
	// The name carries the metric, so a schema whose distance was changed does
	// not silently keep answering through an index built for the old one.
	name := fmt.Sprintf("chunks_embedding_%s_%s", kind, l.dist.name())
	rep := IndexReport{Name: name, Kind: kind}

	var exists bool
	if err := l.pool.QueryRow(ctx,
		`SELECT count(*) > 0 FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`,
		l.schema, name).Scan(&exists); err != nil {
		return rep, fmt.Errorf("pgvector: %w", err)
	}
	if exists {
		rep.Reason = "the index already exists; rebuilding one is hours of work nobody asked for"
		return rep, nil
	}

	// Counted over the view rather than the table: rows belonging to a load
	// that never finished are about to be swept, and sizing an index on them
	// would be sizing it on data that is leaving.
	if err := l.pool.QueryRow(ctx,
		l.q(`SELECT count(*) FROM {s}.loaded_chunks WHERE embedding IS NOT NULL`)).Scan(&rep.Rows); err != nil {
		return rep, fmt.Errorf("pgvector: %w", err)
	}
	minRows := opts.MinRows
	if minRows <= 0 {
		minRows = DefaultIndexMinRows
	}
	if rep.Rows < minRows {
		rep.Reason = fmt.Sprintf("%d embedded chunks is below the %d this would need to pay for itself; "+
			"a sequential scan over a table this size is faster than an index walk, and an ivfflat fitted to it "+
			"would learn centroids from a sample that is not the corpus. Pass MinRows to override",
			rep.Rows, minRows)
		return rep, nil
	}

	var with string
	switch kind {
	case IndexHNSW:
		if opts.M > 0 && opts.EFConstruction > 0 {
			with = fmt.Sprintf(" WITH (m = %d, ef_construction = %d)", opts.M, opts.EFConstruction)
		}
	case IndexIVFFlat:
		rep.Lists = opts.Lists
		if rep.Lists <= 0 {
			rep.Lists = listsFor(rep.Rows)
		}
		with = fmt.Sprintf(" WITH (lists = %d)", rep.Lists)
	}
	sql := fmt.Sprintf("CREATE INDEX %s ON %s.chunks USING %s (embedding %s)%s",
		name, l.schema, kind, opClass, with)
	if _, err := l.pool.Exec(ctx, sql); err != nil {
		return rep, fmt.Errorf("pgvector: %s: %w", sql, err)
	}
	rep.Created = true
	rep.Reason = fmt.Sprintf("built over %d embedded chunks", rep.Rows)
	return rep, nil
}

// listsFor is pgvector's own sizing guidance rather than a number invented
// here: rows/1000 up to a million rows, sqrt(rows) above it. It is derived
// rather than defaulted because "lists = 100" is the single commonest way an
// ivfflat index ends up quietly returning the wrong neighbours — on ten million
// rows that is a hundred thousand vectors per list, of which one probe reads
// one list.
func listsFor(rows int) int {
	n := rows / 1000
	if rows > 1_000_000 {
		n = int(math.Sqrt(float64(rows)))
	}
	return max(n, 1)
}

// name is the metric as it appears in an index name.
func (d Distance) name() string {
	if d == "" {
		return string(Cosine)
	}
	return string(d)
}
